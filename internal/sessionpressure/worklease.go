package sessionpressure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
)

const (
	// State schema 8 adds the ordered operator override queue, so one confirmed
	// request can pin a whole drain order instead of a single waiter. It is
	// additive; n-1 readers still get a schema-7 document through the downgrade
	// writer below, which costs them the pending tail but never the active head.
	workStateSchemaVersion       = 8
	legacyWorkOwnerIdentityToken = "legacy-pid-only"
	// workReservationMinimumSchema is the oldest persisted state shape that can
	// round-trip a non-consuming pressure reservation (reservation_kind and
	// reserved_at on a waiter). Reservations are refused below it and allowed at
	// or above it, independent of how far the schema has since advanced.
	workReservationMinimumSchema = 6
	WorkSchedulingPolicy         = "bounded-lookahead-v1"
	WorkSchedulingPolicyFIFO     = "fifo-v1"
	workSelectorScanLimit        = 64
	workMaximumBypasses          = 4
	// Schema 7 adds non-consuming pressure reservations. A post-acquire host
	// gate no longer charges weighted capacity while it waits to start.
	workSelectorSchemaVersion = 7
)

// WorkMaximumBypasses is the operator-visible admission bound for one blocked
// queue head. Help and other public contracts derive their wording from this
// constant so the starvation guarantee cannot silently drift from policy.
const WorkMaximumBypasses = workMaximumBypasses

// WorkFiniteLeaseReviewAge is advisory only: finite work is never killed for
// age, but operators need a typed signal when it likely contains a resident
// service and can make a queued class infeasible indefinitely.
const WorkFiniteLeaseReviewAge = 15 * time.Minute

// Heavy commands normally run for minutes. A two-second retry keeps queued
// agents responsive without turning every waiter into a 4 Hz lock/read loop on
// an already constrained host.
const workCapacityRetryInterval = 2 * time.Second
const maximumWorkClockSkew = time.Minute

// WorkReservationAge bounds how long a bypassed queue head can remain soft.
// Queue age alone is deliberately not protection: charging every aged
// successor made a backlog collapse back into strict FIFO after this interval.
// Help derives its wording from this value so the operator contract cannot
// silently drift from scheduler policy.
const WorkReservationAge = 30 * time.Second

var (
	ErrWorkCapacity       = errors.New("host heavy-work capacity unavailable")
	ErrWorkFairness       = errors.New("host heavy-work fairness reservation unavailable")
	ErrWorkReservation    = errors.New("host heavy-work protected reservation unavailable")
	ErrWorkUpgradePending = errors.New("host heavy-work state upgrade is waiting for legacy leases to finish")
)

// WorkClass is a stable resource-cost class, not an executable name. Keeping
// this vocabulary small lets builds, emulators, and browser automation contend
// honestly for the same host without growing new top-level command surfaces.
type WorkClass string

const (
	WorkClassTest               WorkClass = "test"
	WorkClassBuild              WorkClass = "build"
	WorkClassExpressTest        WorkClass = "express-test"
	WorkClassExpressBuild       WorkClass = "express-build"
	WorkClassInstall            WorkClass = "install"
	WorkClassEmulator           WorkClass = "emulator"
	WorkClassBrowser            WorkClass = "browser"
	WorkClassHeavy              WorkClass = "heavy"
	WorkClassBenchmark          WorkClass = "benchmark"
	WorkClassBenchmarkExclusive WorkClass = "benchmark-exclusive"
	WorkClassReclaim            WorkClass = "reclaim"
)

func ParseWorkClass(value string) (WorkClass, error) {
	class := WorkClass(strings.ToLower(strings.TrimSpace(value)))
	switch class {
	case WorkClassTest, WorkClassBuild, WorkClassExpressTest, WorkClassExpressBuild,
		WorkClassInstall, WorkClassEmulator, WorkClassBrowser, WorkClassHeavy, WorkClassBenchmark,
		WorkClassBenchmarkExclusive, WorkClassReclaim:
		return class, nil
	default:
		return "", fmt.Errorf("unknown work class %q; want test, build, express-test, express-build, install, emulator, browser, heavy, benchmark, benchmark-exclusive, or reclaim", value)
	}
}

func (limits WorkLimits) Weight(class WorkClass) (int, error) {
	limits = normalizeWorkLimits(limits)
	switch class {
	case WorkClassTest:
		return limits.TestWeight, nil
	case WorkClassBuild:
		return limits.BuildWeight, nil
	case WorkClassExpressTest:
		return limits.ExpressTestWeight, nil
	case WorkClassExpressBuild:
		return limits.ExpressBuildWeight, nil
	case WorkClassEmulator:
		return limits.EmulatorWeight, nil
	case WorkClassBrowser:
		return limits.BrowserWeight, nil
	case WorkClassHeavy:
		return limits.HeavyWeight, nil
	case WorkClassBenchmark:
		// Daily-driver benchmarks leave residual capacity for express work.
		// Clean-host exclusive evidence uses WorkClassBenchmarkExclusive.
		return limits.BenchmarkWeight, nil
	case WorkClassBenchmarkExclusive:
		return limits.Capacity, nil
	case WorkClassInstall:
		return limits.InstallWeight, nil
	case WorkClassReclaim:
		return limits.ReclaimWeight, nil
	default:
		return 0, fmt.Errorf("unknown work class %q", class)
	}
}

// AllWorkClasses is the stable class inventory for stats, attribution, and
// evaluation. Order is operator-facing and deterministic.
func AllWorkClasses() []WorkClass {
	return []WorkClass{
		WorkClassExpressTest, WorkClassTest, WorkClassExpressBuild, WorkClassBuild,
		WorkClassInstall, WorkClassBrowser, WorkClassEmulator, WorkClassHeavy, WorkClassBenchmark,
		WorkClassBenchmarkExclusive, WorkClassReclaim,
	}
}

// WorkLeaseRecord is the private durable owner record. It deliberately omits
// command lines, which can contain source paths, prompts, and credentials.
type WorkLeaseRecord struct {
	ID                 string    `json:"id"`
	OperationID        string    `json:"operation_id"`
	Class              WorkClass `json:"class"`
	Weight             int       `json:"weight"`
	PID                int       `json:"pid"`
	OwnerIdentity      string    `json:"owner_identity"`
	SupervisorPID      int       `json:"supervisor_pid"`
	SupervisorIdentity string    `json:"supervisor_identity"`
	QueuedAt           time.Time `json:"queued_at"`
	StartedAt          time.Time `json:"started_at"`
}

// WorkLeaseStatus keeps the established public v1 wire contract. The private
// process-start token is needed only for reconciliation and is not exposed.
type WorkLeaseStatus struct {
	ID           string    `json:"id"`
	OperationID  string    `json:"operation_id"`
	Class        WorkClass `json:"class"`
	Weight       int       `json:"weight"`
	PID          int       `json:"pid"`
	StartedAt    time.Time `json:"started_at"`
	AgeMS        int64     `json:"age_ms"`
	Review       bool      `json:"review"`
	ReviewReason string    `json:"review_reason,omitempty"`
}

// WorkWaiterRecord is private durable queue state. Like leases, it stores a
// PID plus kernel start identity so dead wrappers and PID reuse cannot retain
// a fairness reservation.
type WorkWaiterRecord struct {
	OperationID   string    `json:"operation_id"`
	Class         WorkClass `json:"class"`
	Weight        int       `json:"weight"`
	PID           int       `json:"pid"`
	OwnerIdentity string    `json:"owner_identity"`
	QueuedAt      time.Time `json:"queued_at"`
	HeartbeatAt   time.Time `json:"heartbeat_at"`
	BypassCount   int       `json:"bypass_count,omitempty"`
	// LastBypassedAt is the retained schema-3 wire name. It records the first
	// bypass and is intentionally set once so the age reservation cannot slide.
	LastBypassedAt  *time.Time `json:"last_bypassed_at,omitempty"`
	ProtectedAt     *time.Time `json:"protected_at,omitempty"`
	ReservationKind string     `json:"reservation_kind,omitempty"`
	ReservedAt      *time.Time `json:"reserved_at,omitempty"`
}

type WorkWaiterStatus struct {
	OperationID      string     `json:"operation_id"`
	Class            WorkClass  `json:"class"`
	Weight           int        `json:"weight"`
	PID              int        `json:"pid"`
	QueuedAt         time.Time  `json:"queued_at"`
	HeartbeatAt      time.Time  `json:"heartbeat_at"`
	Position         int        `json:"position"`
	WaitMS           int64      `json:"wait_ms"`
	BypassCount      int        `json:"bypass_count"`
	Protected        bool       `json:"protected"`
	ProtectionReason string     `json:"protection_reason,omitempty"`
	ReservationKind  string     `json:"reservation_kind,omitempty"`
	ReservedAt       *time.Time `json:"reserved_at,omitempty"`
	// OverridePosition is this waiter's 1-based place in the operator override
	// sequence; 1 is the active head. Zero means the waiter is not pinned.
	OverridePosition int `json:"override_position,omitempty"`
}

type workState struct {
	SchemaVersion       int                `json:"schema_version"`
	Leases              []WorkLeaseRecord  `json:"leases"`
	Waiters             []WorkWaiterRecord `json:"waiters"`
	OverrideOperationID string             `json:"override_operation_id,omitempty"`
	OverrideRequestedAt *time.Time         `json:"override_requested_at,omitempty"`
	// OverrideQueue is the ordered tail of a confirmed operator promotion
	// sequence, excluding the active head above. Entries are promoted into the
	// head slot as each pinned waiter acquires, cancels, or expires.
	OverrideQueue []string `json:"override_queue,omitempty"`
	// Schema 7 adds pre-queue admission holds and the shared CPU latch. Both are
	// observability state: neither charges weighted capacity.
	AdmissionHolds       []WorkAdmissionHoldRecord `json:"admission_holds,omitempty"`
	AdmissionLatch       *WorkAdmissionLatch       `json:"admission_latch,omitempty"`
	prunedAdmissionHolds int
	migrated             bool
	legacyActive         bool
	lastSelection        workSelection
	lastShadowSelection  workSelection
}

type WorkStatus struct {
	SchemaVersion              int                `json:"schema_version"`
	Capacity                   int                `json:"capacity"`
	Used                       int                `json:"used"`
	Available                  int                `json:"available"`
	Leases                     []WorkLeaseStatus  `json:"leases"`
	Waiters                    []WorkWaiterStatus `json:"waiters"`
	QueueDepth                 int                `json:"queue_depth"`
	Pruned                     int                `json:"pruned,omitempty"`
	PrunedWaiters              int                `json:"pruned_waiters,omitempty"`
	StatePath                  string             `json:"state_path"`
	SchedulingPolicy           string             `json:"scheduling_policy"`
	SelectorSchemaVersion      int                `json:"selector_schema_version"`
	SelectedOperationID        string             `json:"selected_operation_id,omitempty"`
	ProtectedOperationID       string             `json:"protected_operation_id,omitempty"`
	DecisionReason             string             `json:"decision_reason,omitempty"`
	BypassedCount              int                `json:"bypassed_count,omitempty"`
	CandidateSchedulingPolicy  string             `json:"candidate_scheduling_policy,omitempty"`
	ShadowSelectedOperationID  string             `json:"shadow_selected_operation_id,omitempty"`
	ShadowProtectedOperationID string             `json:"shadow_protected_operation_id,omitempty"`
	ShadowDecisionReason       string             `json:"shadow_decision_reason,omitempty"`
	OverrideOperationID        string             `json:"override_operation_id,omitempty"`
	OverrideRequestedAt        *time.Time         `json:"override_requested_at,omitempty"`
	// OverrideQueue is the pending tail of the operator promotion sequence and
	// OverrideQueueDepth counts the whole pinned set including the active head.
	OverrideQueue            []string `json:"override_queue,omitempty"`
	OverrideQueueDepth       int      `json:"override_queue_depth,omitempty"`
	PressureReservationCount int      `json:"pressure_reservation_count"`
	ReservedWeight           int      `json:"reserved_weight"`
	// AdmissionHolds are processes parked at the host-pressure gate before queue
	// registration. They hold no capacity, so they never appear in Used, but
	// omitting them made the guard report an empty queue while it blocked work.
	AdmissionHolds       []WorkAdmissionHoldStatus `json:"admission_holds"`
	AdmissionHoldCount   int                       `json:"admission_hold_count"`
	PrunedAdmissionHolds int                       `json:"pruned_admission_holds,omitempty"`
	LongestAdmissionHold int64                     `json:"longest_admission_hold_ms,omitempty"`
	AdmissionLatch       *WorkAdmissionLatch       `json:"admission_latch,omitempty"`
}

const WorkReservationPressure = "pressure"

type WorkCoordinator struct {
	Dir          string
	Limits       WorkLimits
	PID          int
	Now          func() time.Time
	ProcessAlive func(int) bool
	// ProcessIdentity returns a stable, per-process-lifetime token. Combining
	// it with PID prevents an unrelated process that reuses a dead owner's PID
	// from retaining weighted capacity.
	ProcessIdentity func(int) (string, error)
	EventStore      *WorkEventStore
	// SchedulingPolicy remains FIFO until replay promotion gates pass. Tests and
	// an explicit future rollout may select bounded-lookahead-v1.
	SchedulingPolicy string
}

func NewWorkCoordinator(dir string, limits WorkLimits) *WorkCoordinator {
	limits = normalizeWorkLimits(limits)
	return &WorkCoordinator{
		Dir: dir, Limits: limits, PID: os.Getpid(), Now: time.Now,
		ProcessAlive: processAlive, ProcessIdentity: processStartIdentity, EventStore: NewWorkEventStore(dir), SchedulingPolicy: limits.SchedulingPolicy,
	}
}

// workGreenExpressWindow is verified host headroom that lets express-class
// work admit without queue-blocking. The zero value is inactive: express then
// waits exactly as before. It relaxes admission ORDER and grants a small
// bounded express overcommit; it never changes weights, the persisted
// capacity, host-pressure admission, or exclusive benchmark drains.
type workGreenExpressWindow struct {
	Active     bool
	Overcommit int
}

const (
	// workGreenExpressOvercommit bounds how far express work may exceed the
	// weighted capacity while the host is verifiably green: at most one
	// express-build (2) or two express-tests beyond a full ledger.
	workGreenExpressOvercommit = 2
	// workGreenExpressMaxHostCPU keeps the window closed while the host is
	// already compute-saturated, where even weight-1 work adds real contention.
	workGreenExpressMaxHostCPU = 70.0
	// workGreenExpressMaxSampleAge matches the admission freshness bound for
	// the default 90s resident cadence (2×interval+15s headroom).
	workGreenExpressMaxSampleAge = 200 * time.Second
)

// WorkMaximumGreenExpressOvercommit is the public audit ceiling for the
// verified-green express lane. Any ledger weight above capacity is valid only
// when it is no larger than this bound and is attributable to express leases.
const WorkMaximumGreenExpressOvercommit = workGreenExpressOvercommit

// greenExpressWindow reads the resident's freshest sample and reports whether
// the host has verified headroom: normal level, fresh sample, host CPU below
// the ceiling, and memory not in rapid decline. Missing, stale, or degraded
// host evidence deactivates the window. A guard budget failure during resident
// warm-up keeps only the safe residual express lane: it never grants
// overcommit. Once the guard baseline is proven, a budget failure closes the
// lane. NDEV_PRESSURE_NO_GREEN_EXPRESS=1 is the operator kill-switch.
func (coordinator *WorkCoordinator) greenExpressWindow() workGreenExpressWindow {
	if coordinator == nil || os.Getenv("NDEV_PRESSURE_NO_GREEN_EXPRESS") == "1" {
		return workGreenExpressWindow{}
	}
	snapshot, ok := NewTelemetryStore(coordinator.Dir).ReadLatest()
	if !ok || snapshot.Timestamp.IsZero() {
		return workGreenExpressWindow{}
	}
	age := coordinator.now().Sub(snapshot.Timestamp)
	if age < -5*time.Second || age > workGreenExpressMaxSampleAge {
		return workGreenExpressWindow{}
	}
	if snapshot.Level != LevelNormal {
		return workGreenExpressWindow{}
	}
	if snapshot.HostCPUPercent >= workGreenExpressMaxHostCPU {
		return workGreenExpressWindow{}
	}
	if snapshot.MemoryMomentum == MemoryMomentumRapidDecline {
		return workGreenExpressWindow{}
	}
	overcommit := workGreenExpressOvercommit
	if !snapshot.GuardBudgetOK {
		if snapshot.GuardBaselineProven {
			return workGreenExpressWindow{}
		}
		overcommit = 0
	}
	return workGreenExpressWindow{Active: true, Overcommit: overcommit}
}

func (coordinator *WorkCoordinator) recordExpired(operationID, leaseID string, class WorkClass, weight, pid int, outcome string) error {
	store := coordinator.EventStore
	if store == nil {
		store = NewWorkEventStore(coordinator.Dir)
	}
	terminal, err := store.operationHasTerminal(operationID)
	if err != nil {
		return fmt.Errorf("check work operation terminal state: %w", err)
	}
	if terminal {
		// The child may have durably recorded its terminal outcome before a
		// helper/version failure prevented lease cleanup. Reconciliation should
		// remove the stale lease without inventing a second terminal outcome.
		return nil
	}
	return store.AppendDurable(WorkEvent{
		Event: WorkEventExpired, OperationID: operationID, LeaseID: leaseID,
		Class: class, Weight: weight, PID: pid, Outcome: outcome,
	})
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (coordinator *WorkCoordinator) statePath() string {
	return filepath.Join(coordinator.Dir, "work-leases.json")
}

func (coordinator *WorkCoordinator) identityForPID(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("work lease PID must be positive")
	}
	identity := coordinator.ProcessIdentity
	if identity == nil {
		identity = processStartIdentity
	}
	token, err := identity(pid)
	if err != nil {
		return "", fmt.Errorf("resolve process identity for PID %d: %w", pid, err)
	}
	token = strings.TrimSpace(token)
	if token == "" || token == legacyWorkOwnerIdentityToken {
		return "", fmt.Errorf("resolve process identity for PID %d: invalid identity token", pid)
	}
	return token, nil
}

func (coordinator *WorkCoordinator) validate() error {
	if strings.TrimSpace(coordinator.Dir) == "" {
		return errors.New("work coordinator directory is required")
	}
	if coordinator.PID <= 0 {
		return errors.New("work coordinator PID must be positive")
	}
	w := coordinator.limits()
	if w.Capacity < 1 || w.TestWeight < 1 || w.BuildWeight < 1 || w.ExpressTestWeight < 1 || w.ExpressBuildWeight < 1 || w.EmulatorWeight < 1 || w.BrowserWeight < 1 || w.HeavyWeight < 1 || w.BenchmarkWeight < 1 {
		return errors.New("work capacity and weights must be positive")
	}
	if w.TestWeight > w.Capacity || w.BuildWeight > w.Capacity || w.ExpressTestWeight > w.Capacity || w.ExpressBuildWeight > w.Capacity || w.EmulatorWeight > w.Capacity || w.BrowserWeight > w.Capacity || w.HeavyWeight > w.Capacity || w.BenchmarkWeight > w.Capacity {
		return errors.New("work weights cannot exceed capacity")
	}
	return nil
}

// limits returns the effective immutable coordinator policy. Validation used to
// normalize Limits in place, which raced when Status and BindPID were invoked
// concurrently on the same coordinator. Callers may construct a coordinator
// directly in tests and integrations, so normalize on read without mutation.
func (coordinator *WorkCoordinator) limits() WorkLimits {
	return normalizeWorkLimits(coordinator.Limits)
}

func (coordinator *WorkCoordinator) readState() (workState, error) {
	path := coordinator.statePath()
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return workState{SchemaVersion: workStateSchemaVersion}, nil
		}
		return workState{}, fmt.Errorf("read work lease state: %w", err)
	}
	var state workState
	if err := json.Unmarshal(body, &state); err != nil {
		return workState{}, fmt.Errorf("decode work lease state: %w", err)
	}
	if state.SchemaVersion < 1 || state.SchemaVersion > workStateSchemaVersion {
		return workState{}, fmt.Errorf("unsupported work lease schema_version %d", state.SchemaVersion)
	}
	if state.SchemaVersion == 1 {
		// Version 1 recorded only PID. Preserve live legacy work conservatively
		// rather than blessing the identity of whatever process currently owns a
		// reused PID. New and rebound leases always receive an exact start token.
		for index := range state.Leases {
			if strings.TrimSpace(state.Leases[index].OwnerIdentity) != "" {
				return workState{}, errors.New("schema_version 1 work lease unexpectedly contains owner_identity")
			}
			state.Leases[index].OwnerIdentity = legacyWorkOwnerIdentityToken
		}
	}
	if state.SchemaVersion < workStateSchemaVersion {
		for index := range state.Leases {
			if state.Leases[index].OperationID == "" {
				state.Leases[index].OperationID = state.Leases[index].ID
			}
			if state.Leases[index].QueuedAt.IsZero() {
				state.Leases[index].QueuedAt = state.Leases[index].StartedAt
			}
		}
		// Schemas 1 and 2 predate the durable queue. Schema 3 already owns
		// waiters, so preserve those records until every schema-3 owner drains.
		if state.SchemaVersion < 3 {
			state.Waiters = []WorkWaiterRecord{}
		}
		if len(state.Leases) > 0 || len(state.Waiters) > 0 {
			// Do not publish a schema the installed n-1 helper cannot read while
			// that helper still owns a lease. This makes in-place binary upgrades
			// safe even when another session reads status during the build.
			state.legacyActive = true
		}
	}
	return state, nil
}

func (coordinator *WorkCoordinator) writeState(state workState) error {
	if state.SchemaVersion < workStateSchemaVersion {
		type legacyLeaseV1 struct {
			ID        string    `json:"id"`
			Class     WorkClass `json:"class"`
			Weight    int       `json:"weight"`
			PID       int       `json:"pid"`
			StartedAt time.Time `json:"started_at"`
		}
		type legacyLeaseV2 struct {
			ID            string    `json:"id"`
			Class         WorkClass `json:"class"`
			Weight        int       `json:"weight"`
			PID           int       `json:"pid"`
			OwnerIdentity string    `json:"owner_identity"`
			StartedAt     time.Time `json:"started_at"`
		}
		type legacyLeaseV3 struct {
			ID                 string    `json:"id"`
			OperationID        string    `json:"operation_id"`
			Class              WorkClass `json:"class"`
			Weight             int       `json:"weight"`
			PID                int       `json:"pid"`
			OwnerIdentity      string    `json:"owner_identity"`
			SupervisorPID      int       `json:"supervisor_pid"`
			SupervisorIdentity string    `json:"supervisor_identity"`
			StartedAt          time.Time `json:"started_at"`
		}
		type legacyWaiterV3 struct {
			OperationID   string    `json:"operation_id"`
			Class         WorkClass `json:"class"`
			Weight        int       `json:"weight"`
			PID           int       `json:"pid"`
			OwnerIdentity string    `json:"owner_identity"`
			QueuedAt      time.Time `json:"queued_at"`
			HeartbeatAt   time.Time `json:"heartbeat_at"`
		}
		type legacyWaiterV4 struct {
			OperationID    string     `json:"operation_id"`
			Class          WorkClass  `json:"class"`
			Weight         int        `json:"weight"`
			PID            int        `json:"pid"`
			OwnerIdentity  string     `json:"owner_identity"`
			QueuedAt       time.Time  `json:"queued_at"`
			HeartbeatAt    time.Time  `json:"heartbeat_at"`
			BypassCount    int        `json:"bypass_count,omitempty"`
			LastBypassedAt *time.Time `json:"last_bypassed_at,omitempty"`
			ProtectedAt    *time.Time `json:"protected_at,omitempty"`
		}
		type legacyStateV5 struct {
			SchemaVersion       int              `json:"schema_version"`
			Leases              []legacyLeaseV3  `json:"leases"`
			Waiters             []legacyWaiterV4 `json:"waiters"`
			OverrideOperationID string           `json:"override_operation_id,omitempty"`
			OverrideRequestedAt *time.Time       `json:"override_requested_at,omitempty"`
		}
		if state.SchemaVersion == 1 {
			legacy := struct {
				SchemaVersion int             `json:"schema_version"`
				Leases        []legacyLeaseV1 `json:"leases"`
			}{SchemaVersion: 1, Leases: make([]legacyLeaseV1, 0, len(state.Leases))}
			for _, lease := range state.Leases {
				legacy.Leases = append(legacy.Leases, legacyLeaseV1{
					ID: lease.ID, Class: lease.Class, Weight: lease.Weight, PID: lease.PID, StartedAt: lease.StartedAt,
				})
			}
			body, err := json.MarshalIndent(legacy, "", "  ")
			if err != nil {
				return fmt.Errorf("encode schema-1-compatible work lease state: %w", err)
			}
			if err := atomicWrite(coordinator.statePath(), append(body, '\n'), 0o600); err != nil {
				return fmt.Errorf("write schema-1-compatible work lease state: %w", err)
			}
			return nil
		}
		if state.SchemaVersion == 3 {
			legacy := struct {
				SchemaVersion int              `json:"schema_version"`
				Leases        []legacyLeaseV3  `json:"leases"`
				Waiters       []legacyWaiterV3 `json:"waiters"`
			}{SchemaVersion: 3, Leases: make([]legacyLeaseV3, 0, len(state.Leases)), Waiters: make([]legacyWaiterV3, 0, len(state.Waiters))}
			for _, lease := range state.Leases {
				legacy.Leases = append(legacy.Leases, legacyLeaseV3{
					ID: lease.ID, OperationID: lease.OperationID, Class: lease.Class, Weight: lease.Weight,
					PID: lease.PID, OwnerIdentity: lease.OwnerIdentity, SupervisorPID: lease.SupervisorPID,
					SupervisorIdentity: lease.SupervisorIdentity, StartedAt: lease.StartedAt,
				})
			}
			for _, waiter := range state.Waiters {
				legacy.Waiters = append(legacy.Waiters, legacyWaiterV3{
					OperationID: waiter.OperationID, Class: waiter.Class, Weight: waiter.Weight, PID: waiter.PID,
					OwnerIdentity: waiter.OwnerIdentity, QueuedAt: waiter.QueuedAt, HeartbeatAt: waiter.HeartbeatAt,
				})
			}
			body, err := json.MarshalIndent(legacy, "", "  ")
			if err != nil {
				return fmt.Errorf("encode schema-3-compatible work lease state: %w", err)
			}
			if err := atomicWrite(coordinator.statePath(), append(body, '\n'), 0o600); err != nil {
				return fmt.Errorf("write schema-3-compatible work lease state: %w", err)
			}
			return nil
		}
		if state.SchemaVersion == 4 {
			legacy := struct {
				SchemaVersion int              `json:"schema_version"`
				Leases        []legacyLeaseV3  `json:"leases"`
				Waiters       []legacyWaiterV4 `json:"waiters"`
			}{SchemaVersion: 4, Leases: make([]legacyLeaseV3, 0, len(state.Leases)), Waiters: make([]legacyWaiterV4, 0, len(state.Waiters))}
			for _, lease := range state.Leases {
				legacy.Leases = append(legacy.Leases, legacyLeaseV3{
					ID: lease.ID, OperationID: lease.OperationID, Class: lease.Class, Weight: lease.Weight,
					PID: lease.PID, OwnerIdentity: lease.OwnerIdentity, SupervisorPID: lease.SupervisorPID,
					SupervisorIdentity: lease.SupervisorIdentity, StartedAt: lease.StartedAt,
				})
			}
			for _, waiter := range state.Waiters {
				legacy.Waiters = append(legacy.Waiters, legacyWaiterV4{
					OperationID: waiter.OperationID, Class: waiter.Class, Weight: waiter.Weight, PID: waiter.PID,
					OwnerIdentity: waiter.OwnerIdentity, QueuedAt: waiter.QueuedAt, HeartbeatAt: waiter.HeartbeatAt,
					BypassCount: waiter.BypassCount, LastBypassedAt: waiter.LastBypassedAt, ProtectedAt: waiter.ProtectedAt,
				})
			}
			body, err := json.MarshalIndent(legacy, "", "  ")
			if err != nil {
				return fmt.Errorf("encode schema-4-compatible work lease state: %w", err)
			}
			if err := atomicWrite(coordinator.statePath(), append(body, '\n'), 0o600); err != nil {
				return fmt.Errorf("write schema-4-compatible work lease state: %w", err)
			}
			return nil
		}
		if state.SchemaVersion == 5 {
			legacy := legacyStateV5{
				SchemaVersion: 5, Leases: make([]legacyLeaseV3, 0, len(state.Leases)), Waiters: make([]legacyWaiterV4, 0, len(state.Waiters)),
				OverrideOperationID: state.OverrideOperationID, OverrideRequestedAt: state.OverrideRequestedAt,
			}
			for _, lease := range state.Leases {
				legacy.Leases = append(legacy.Leases, legacyLeaseV3{
					ID: lease.ID, OperationID: lease.OperationID, Class: lease.Class, Weight: lease.Weight,
					PID: lease.PID, OwnerIdentity: lease.OwnerIdentity, SupervisorPID: lease.SupervisorPID,
					SupervisorIdentity: lease.SupervisorIdentity, StartedAt: lease.StartedAt,
				})
			}
			for _, waiter := range state.Waiters {
				legacy.Waiters = append(legacy.Waiters, legacyWaiterV4{
					OperationID: waiter.OperationID, Class: waiter.Class, Weight: waiter.Weight, PID: waiter.PID,
					OwnerIdentity: waiter.OwnerIdentity, QueuedAt: waiter.QueuedAt, HeartbeatAt: waiter.HeartbeatAt,
					BypassCount: waiter.BypassCount, LastBypassedAt: waiter.LastBypassedAt, ProtectedAt: waiter.ProtectedAt,
				})
			}
			body, err := json.MarshalIndent(legacy, "", "  ")
			if err != nil {
				return fmt.Errorf("encode schema-5-compatible work lease state: %w", err)
			}
			if err := atomicWrite(coordinator.statePath(), append(body, '\n'), 0o600); err != nil {
				return fmt.Errorf("write schema-5-compatible work lease state: %w", err)
			}
			return nil
		}
		if state.SchemaVersion == 6 {
			// Schema 6 knows leases, waiters, and overrides but not admission
			// holds or the shared latch. Both are observability-only, so a
			// schema-6 reader loses visibility, never correctness.
			legacy := struct {
				SchemaVersion       int                `json:"schema_version"`
				Leases              []WorkLeaseRecord  `json:"leases"`
				Waiters             []WorkWaiterRecord `json:"waiters"`
				OverrideOperationID string             `json:"override_operation_id,omitempty"`
				OverrideRequestedAt *time.Time         `json:"override_requested_at,omitempty"`
			}{
				SchemaVersion: 6, Leases: state.Leases, Waiters: state.Waiters,
				OverrideOperationID: state.OverrideOperationID, OverrideRequestedAt: state.OverrideRequestedAt,
			}
			if legacy.Leases == nil {
				legacy.Leases = []WorkLeaseRecord{}
			}
			if legacy.Waiters == nil {
				legacy.Waiters = []WorkWaiterRecord{}
			}
			body, err := json.MarshalIndent(legacy, "", "  ")
			if err != nil {
				return fmt.Errorf("encode schema-6-compatible work lease state: %w", err)
			}
			if err := atomicWrite(coordinator.statePath(), append(body, '\n'), 0o600); err != nil {
				return fmt.Errorf("write schema-6-compatible work lease state: %w", err)
			}
			return nil
		}
		if state.SchemaVersion == 7 {
			// Schema 7 knows a single override head but not the ordered tail. A
			// schema-7 owner therefore keeps the active promotion and falls back
			// to ordinary policy for the rest — degraded ordering, never a lost
			// or misattributed lease.
			legacy := struct {
				SchemaVersion       int                       `json:"schema_version"`
				Leases              []WorkLeaseRecord         `json:"leases"`
				Waiters             []WorkWaiterRecord        `json:"waiters"`
				OverrideOperationID string                    `json:"override_operation_id,omitempty"`
				OverrideRequestedAt *time.Time                `json:"override_requested_at,omitempty"`
				AdmissionHolds      []WorkAdmissionHoldRecord `json:"admission_holds,omitempty"`
				AdmissionLatch      *WorkAdmissionLatch       `json:"admission_latch,omitempty"`
			}{
				SchemaVersion: 7, Leases: state.Leases, Waiters: state.Waiters,
				OverrideOperationID: state.OverrideOperationID, OverrideRequestedAt: state.OverrideRequestedAt,
				AdmissionHolds: state.AdmissionHolds, AdmissionLatch: state.AdmissionLatch,
			}
			if legacy.Leases == nil {
				legacy.Leases = []WorkLeaseRecord{}
			}
			if legacy.Waiters == nil {
				legacy.Waiters = []WorkWaiterRecord{}
			}
			body, err := json.MarshalIndent(legacy, "", "  ")
			if err != nil {
				return fmt.Errorf("encode schema-7-compatible work lease state: %w", err)
			}
			if err := atomicWrite(coordinator.statePath(), append(body, '\n'), 0o600); err != nil {
				return fmt.Errorf("write schema-7-compatible work lease state: %w", err)
			}
			return nil
		}
		legacy := struct {
			SchemaVersion int             `json:"schema_version"`
			Leases        []legacyLeaseV2 `json:"leases"`
		}{SchemaVersion: 2, Leases: make([]legacyLeaseV2, 0, len(state.Leases))}
		for _, lease := range state.Leases {
			legacy.Leases = append(legacy.Leases, legacyLeaseV2{
				ID: lease.ID, Class: lease.Class, Weight: lease.Weight, PID: lease.PID,
				OwnerIdentity: lease.OwnerIdentity, StartedAt: lease.StartedAt,
			})
		}
		body, err := json.MarshalIndent(legacy, "", "  ")
		if err != nil {
			return fmt.Errorf("encode legacy-compatible work lease state: %w", err)
		}
		if err := atomicWrite(coordinator.statePath(), append(body, '\n'), 0o600); err != nil {
			return fmt.Errorf("write legacy-compatible work lease state: %w", err)
		}
		return nil
	}
	state.SchemaVersion = workStateSchemaVersion
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode work lease state: %w", err)
	}
	body = append(body, '\n')
	if err := atomicWrite(coordinator.statePath(), body, 0o600); err != nil {
		return fmt.Errorf("write work lease state: %w", err)
	}
	return nil
}

func (coordinator *WorkCoordinator) reconcile(state *workState) (prunedLeases, prunedWaiters int, changed bool, err error) {
	alive := coordinator.ProcessAlive
	if alive == nil {
		alive = processAlive
	}
	identity := coordinator.ProcessIdentity
	if identity == nil {
		identity = processStartIdentity
	}
	seenLeaseIDs := make(map[string]struct{}, len(state.Leases))
	seenOperations := make(map[string]struct{}, len(state.Leases)+len(state.Waiters))
	keptLeases := state.Leases[:0]
	changed = state.migrated
	now := coordinator.now()
	for _, lease := range state.Leases {
		if lease.SupervisorPID <= 0 {
			lease.SupervisorPID = lease.PID
			changed = changed || !state.legacyActive
		}
		if strings.TrimSpace(lease.SupervisorIdentity) == "" {
			lease.SupervisorIdentity = lease.OwnerIdentity
			changed = changed || !state.legacyActive
		}
		if !validPrivateID(lease.ID) {
			return 0, 0, false, fmt.Errorf("invalid work lease id %q", lease.ID)
		}
		if !validPrivateID(lease.OperationID) {
			return 0, 0, false, fmt.Errorf("invalid work operation id %q", lease.OperationID)
		}
		if _, duplicate := seenLeaseIDs[lease.ID]; duplicate {
			return 0, 0, false, fmt.Errorf("duplicate work lease id %q", lease.ID)
		}
		if _, duplicate := seenOperations[lease.OperationID]; duplicate {
			return 0, 0, false, fmt.Errorf("duplicate work operation id %q", lease.OperationID)
		}
		seenLeaseIDs[lease.ID] = struct{}{}
		seenOperations[lease.OperationID] = struct{}{}
		expectedWeight, weightErr := coordinator.limits().Weight(lease.Class)
		if weightErr != nil {
			return 0, 0, false, fmt.Errorf("invalid work lease %s: %w", lease.ID, weightErr)
		}
		if lease.PID <= 0 || lease.SupervisorPID <= 0 || lease.QueuedAt.IsZero() || lease.StartedAt.IsZero() || lease.QueuedAt.After(lease.StartedAt) || lease.StartedAt.After(now.Add(maximumWorkClockSkew)) || strings.TrimSpace(lease.OwnerIdentity) == "" || strings.TrimSpace(lease.SupervisorIdentity) == "" {
			return 0, 0, false, fmt.Errorf("invalid work lease %s metadata", lease.ID)
		}
		supervisorAlive := alive(lease.SupervisorPID)
		supervisorOwns := false
		if supervisorAlive {
			if lease.SupervisorIdentity == legacyWorkOwnerIdentityToken {
				supervisorOwns = true
			} else {
				currentSupervisorIdentity, supervisorErr := identity(lease.SupervisorPID)
				if supervisorErr != nil {
					return 0, 0, false, fmt.Errorf("resolve work lease %s supervisor identity: %w", lease.ID, supervisorErr)
				}
				if strings.TrimSpace(currentSupervisorIdentity) == "" {
					return 0, 0, false, fmt.Errorf("resolve work lease %s supervisor identity: empty token", lease.ID)
				}
				supervisorOwns = currentSupervisorIdentity == lease.SupervisorIdentity
			}
		}
		if !alive(lease.PID) {
			// The wrapper is the finalization authority. A child can disappear a
			// few milliseconds before Wait returns; retaining the lease while that
			// exact wrapper is alive prevents status/release from manufacturing a
			// false expired event during normal completion.
			if supervisorOwns {
				if lease.Weight != expectedWeight {
					lease.Weight = expectedWeight
					changed = true
				}
				keptLeases = append(keptLeases, lease)
				continue
			}
			if eventErr := coordinator.recordExpired(lease.OperationID, lease.ID, lease.Class, expectedWeight, lease.PID, "dead_lease_owner"); eventErr != nil {
				return 0, 0, false, fmt.Errorf("persist expired work lease %s: %w", lease.ID, eventErr)
			}
			prunedLeases++
			changed = true
			continue
		}
		if lease.OwnerIdentity != legacyWorkOwnerIdentityToken {
			currentIdentity, identityErr := identity(lease.PID)
			if identityErr != nil {
				if !alive(lease.PID) {
					if eventErr := coordinator.recordExpired(lease.OperationID, lease.ID, lease.Class, expectedWeight, lease.PID, "dead_lease_owner"); eventErr != nil {
						return 0, 0, false, fmt.Errorf("persist expired work lease %s: %w", lease.ID, eventErr)
					}
					prunedLeases++
					changed = true
					continue
				}
				return 0, 0, false, fmt.Errorf("resolve work lease %s owner identity: %w", lease.ID, identityErr)
			}
			if strings.TrimSpace(currentIdentity) == "" {
				return 0, 0, false, fmt.Errorf("resolve work lease %s owner identity: empty token", lease.ID)
			}
			if currentIdentity != lease.OwnerIdentity {
				if supervisorOwns {
					if lease.Weight != expectedWeight {
						lease.Weight = expectedWeight
						changed = true
					}
					keptLeases = append(keptLeases, lease)
					continue
				}
				if eventErr := coordinator.recordExpired(lease.OperationID, lease.ID, lease.Class, expectedWeight, lease.PID, "reused_lease_pid"); eventErr != nil {
					return 0, 0, false, fmt.Errorf("persist expired work lease %s: %w", lease.ID, eventErr)
				}
				prunedLeases++
				changed = true
				continue
			}
		}
		if lease.Weight != expectedWeight {
			lease.Weight = expectedWeight
			changed = true
		}
		keptLeases = append(keptLeases, lease)
	}
	state.Leases = keptLeases

	keptWaiters := state.Waiters[:0]
	for _, waiter := range state.Waiters {
		if !validPrivateID(waiter.OperationID) {
			return 0, 0, false, fmt.Errorf("invalid work waiter operation id %q", waiter.OperationID)
		}
		if _, duplicate := seenOperations[waiter.OperationID]; duplicate {
			return 0, 0, false, fmt.Errorf("duplicate work operation id %q", waiter.OperationID)
		}
		seenOperations[waiter.OperationID] = struct{}{}
		expectedWeight, weightErr := coordinator.limits().Weight(waiter.Class)
		if weightErr != nil {
			return 0, 0, false, fmt.Errorf("invalid work waiter %s: %w", waiter.OperationID, weightErr)
		}
		if waiter.PID <= 0 || waiter.QueuedAt.IsZero() || waiter.QueuedAt.After(now.Add(maximumWorkClockSkew)) || waiter.HeartbeatAt.IsZero() || waiter.HeartbeatAt.Before(waiter.QueuedAt) || waiter.HeartbeatAt.After(now.Add(maximumWorkClockSkew)) || strings.TrimSpace(waiter.OwnerIdentity) == "" {
			return 0, 0, false, fmt.Errorf("invalid work waiter %s metadata", waiter.OperationID)
		}
		if waiter.BypassCount < 0 || waiter.BypassCount > workMaximumBypasses {
			return 0, 0, false, fmt.Errorf("invalid work waiter %s bypass_count %d", waiter.OperationID, waiter.BypassCount)
		}
		for label, timestamp := range map[string]*time.Time{"last_bypassed_at": waiter.LastBypassedAt, "protected_at": waiter.ProtectedAt} {
			if timestamp != nil && (timestamp.Before(waiter.QueuedAt) || timestamp.After(now.Add(maximumWorkClockSkew))) {
				return 0, 0, false, fmt.Errorf("invalid work waiter %s %s", waiter.OperationID, label)
			}
		}
		switch waiter.ReservationKind {
		case "":
			if waiter.ReservedAt != nil {
				return 0, 0, false, fmt.Errorf("invalid work waiter %s reserved_at without reservation_kind", waiter.OperationID)
			}
		case WorkReservationPressure:
			if waiter.ReservedAt == nil || waiter.ReservedAt.Before(waiter.QueuedAt) || waiter.ReservedAt.After(now.Add(maximumWorkClockSkew)) {
				return 0, 0, false, fmt.Errorf("invalid work waiter %s pressure reservation metadata", waiter.OperationID)
			}
		default:
			return 0, 0, false, fmt.Errorf("invalid work waiter %s reservation_kind %q", waiter.OperationID, waiter.ReservationKind)
		}
		if waiter.ProtectedAt != nil {
			unmaterialized := waiter
			unmaterialized.Weight = expectedWeight
			unmaterialized.ProtectedAt = nil
			protected, _ := workWaiterProtection(unmaterialized, coordinator.limits().Capacity, now)
			// Before the residual-capacity benchmark class shipped, benchmark
			// waiters were capacity-sized and therefore received an immediate
			// exclusive-drain promise. Preserve that already-materialized promise
			// while normalizing the live waiter to the new residual weight — both
			// before rewrite (weight still capacity) and after (weight already
			// equals expected residual). Restrict to WorkClassBenchmark so an
			// arbitrary stale ProtectedAt cannot mint protection on other classes.
			legacyExclusiveBenchmark := waiter.Class == WorkClassBenchmark &&
				expectedWeight < coordinator.limits().Capacity &&
				(waiter.Weight == coordinator.limits().Capacity || waiter.Weight == expectedWeight)
			if !protected && !legacyExclusiveBenchmark {
				return 0, 0, false, fmt.Errorf("invalid work waiter %s premature protection", waiter.OperationID)
			}
		}
		if !alive(waiter.PID) {
			if eventErr := coordinator.recordExpired(waiter.OperationID, "", waiter.Class, expectedWeight, waiter.PID, "dead_waiter_owner"); eventErr != nil {
				return 0, 0, false, fmt.Errorf("persist expired work waiter %s: %w", waiter.OperationID, eventErr)
			}
			prunedWaiters++
			changed = true
			continue
		}
		currentIdentity, identityErr := identity(waiter.PID)
		if identityErr != nil {
			if !alive(waiter.PID) {
				if eventErr := coordinator.recordExpired(waiter.OperationID, "", waiter.Class, expectedWeight, waiter.PID, "dead_waiter_owner"); eventErr != nil {
					return 0, 0, false, fmt.Errorf("persist expired work waiter %s: %w", waiter.OperationID, eventErr)
				}
				prunedWaiters++
				changed = true
				continue
			}
			return 0, 0, false, fmt.Errorf("resolve work waiter %s owner identity: %w", waiter.OperationID, identityErr)
		}
		if strings.TrimSpace(currentIdentity) == "" {
			return 0, 0, false, fmt.Errorf("resolve work waiter %s owner identity: empty token", waiter.OperationID)
		}
		if currentIdentity != waiter.OwnerIdentity {
			if eventErr := coordinator.recordExpired(waiter.OperationID, "", waiter.Class, expectedWeight, waiter.PID, "reused_waiter_pid"); eventErr != nil {
				return 0, 0, false, fmt.Errorf("persist expired work waiter %s: %w", waiter.OperationID, eventErr)
			}
			prunedWaiters++
			changed = true
			continue
		}
		if waiter.Weight != expectedWeight {
			waiter.Weight = expectedWeight
			changed = true
		}
		keptWaiters = append(keptWaiters, waiter)
	}
	state.Waiters = keptWaiters
	sort.Slice(state.Waiters, func(i, j int) bool {
		if state.Waiters[i].QueuedAt.Equal(state.Waiters[j].QueuedAt) {
			return state.Waiters[i].OperationID < state.Waiters[j].OperationID
		}
		return state.Waiters[i].QueuedAt.Before(state.Waiters[j].QueuedAt)
	})
	if state.legacyActive && len(state.Leases) == 0 && len(state.Waiters) == 0 {
		state.legacyActive = false
		// Keep the empty persisted state on the n-1 schema until a new helper
		// successfully mutates it. A newer read-only CLI may be installed before
		// the tiny resident/work helper; eagerly publishing the new schema here
		// would make the old helper unable to queue the build that upgrades it.
	}
	if advanceWorkOverride(state, coordinator.now()) {
		changed = true
	}
	prunedHolds, holdsChanged, holdErr := coordinator.reconcileAdmissionHolds(state)
	if holdErr != nil {
		return 0, 0, false, holdErr
	}
	if holdsChanged {
		changed = true
	}
	// Holds are counted separately from waiters: conflating them would make
	// pruned_waiters report queue churn that never happened.
	state.prunedAdmissionHolds = prunedHolds
	return prunedLeases, prunedWaiters, changed, nil
}

type workSelection struct {
	SelectedOperationID  string
	ProtectedOperationID string
	DecisionReason       string
	BypassedIndexes      []int
}

func workWaiterProtection(waiter WorkWaiterRecord, capacity int, now time.Time) (bool, string) {
	if waiter.ReservationKind == WorkReservationPressure && waiter.ReservedAt != nil {
		return true, "pressure_reservation"
	}
	// A capacity-sized operation cannot overlap any leased work. Letting smaller
	// successors continue to enter while it is the queue head can prevent the
	// host from ever draining and makes benchmark evidence systematically worse
	// than FIFO. Reserve the drain immediately; this changes admission order,
	// never the operation's resource weight or the hard capacity ceiling.
	if capacity > 0 && waiter.Weight == capacity {
		return true, "exclusive_capacity"
	}
	if waiter.BypassCount >= workMaximumBypasses {
		return true, "bypass_limit"
	}
	if waiter.LastBypassedAt != nil && now.Sub(*waiter.LastBypassedAt) >= WorkReservationAge {
		return true, "bypass_age"
	}
	if waiter.ProtectedAt != nil {
		return true, "materialized"
	}
	return false, ""
}

// selectWorkWaiter is a deterministic, side-effect-free bounded-lookahead
// selector. The caller persists bypass accounting only after this function has
// chosen the caller under the same state lock. Green-window evidence is
// resolved by the caller so selection itself stays pure and replayable.
func selectWorkWaiter(waiters []WorkWaiterRecord, used, capacity int, now time.Time, green workGreenExpressWindow, expressLeased int) workSelection {
	if len(waiters) == 0 {
		return workSelection{DecisionReason: "queue_empty"}
	}
	available := max(0, capacity-used)
	limit := min(len(waiters), workSelectorScanLimit)
	if protected, reason := workWaiterProtection(waiters[0], capacity, now); protected {
		selection := workSelection{ProtectedOperationID: waiters[0].OperationID, DecisionReason: "protected_" + reason}
		if waiters[0].Weight <= available {
			selection.SelectedOperationID = waiters[0].OperationID
			return selection
		}
		if reason == "exclusive_capacity" {
			// Clean-host benchmark evidence: no ride-through, green or not.
			selection.DecisionReason = "protected_exclusive_drain"
			return selection
		}
		if green.Active {
			// Verified-green drain ride-through: an express waiter may use
			// genuinely idle units during the drain only when it provably
			// cannot extend it. The budget is CUMULATIVE: all in-flight
			// express riders plus this candidate plus the protected head must
			// fit inside capacity together, so riders can only ever occupy the
			// residue the head can never use — the head still admits the
			// moment every non-express lease releases, under any sustained
			// express stream. No overcommit inside a drain.
			for index := 1; index < limit; index++ {
				candidate := waiters[index]
				if !IsExpressClass(candidate.Class) {
					continue
				}
				if candidate.Weight > available {
					continue
				}
				if expressLeased+candidate.Weight+waiters[0].Weight > capacity {
					continue
				}
				selection.SelectedOperationID = candidate.OperationID
				selection.DecisionReason = "green_express_drain_ride"
				return selection
			}
		}
		// Protection is a drain reservation, not merely a promise to prefer the
		// head once capacity happens to fit. Continuing to admit feasible
		// successors can keep a 5+3 packing wave permanently full and starve a
		// protected weight-6 head. Leave currently available units idle until
		// existing leases release enough capacity for the protected operation.
		selection.DecisionReason = "protected_bounded_drain"
		return selection
	}
	for index := 0; index < limit; index++ {
		candidate := waiters[index]
		overcommitted := false
		if candidate.Weight > available {
			// Verified-green express overcommit: signed slack so stacked
			// overcommits self-limit at capacity+Overcommit total weight.
			if !green.Active || !IsExpressClass(candidate.Class) || candidate.Weight > capacity+green.Overcommit-used {
				continue
			}
			overcommitted = true
		}
		reason := "queue_head_fits"
		if index > 0 {
			reason = "oldest_feasible_head_bypass"
		}
		if overcommitted {
			reason = "green_express_overcommit"
		}
		selection := workSelection{
			SelectedOperationID: candidate.OperationID,
			DecisionReason:      reason,
		}
		if index > 0 {
			// Only the active blocking head accrues a bypass. Successors have not
			// yet had their own bounded opportunity to use otherwise idle capacity.
			selection.BypassedIndexes = []int{0}
		}
		return selection
	}
	return workSelection{DecisionReason: "no_waiter_fits_capacity"}
}

func selectFIFOWorkWaiter(waiters []WorkWaiterRecord, used, capacity int, now time.Time, green workGreenExpressWindow) workSelection {
	if len(waiters) == 0 {
		return workSelection{DecisionReason: "queue_empty"}
	}
	protectedOperationID := ""
	if protected, _ := workWaiterProtection(waiters[0], capacity, now); protected {
		protectedOperationID = waiters[0].OperationID
	}
	if used+waiters[0].Weight > capacity {
		// FIFO order is preserved; green only relaxes the express head's
		// capacity by the bounded overcommit.
		if green.Active && IsExpressClass(waiters[0].Class) && used+waiters[0].Weight <= capacity+green.Overcommit {
			return workSelection{SelectedOperationID: waiters[0].OperationID, ProtectedOperationID: protectedOperationID, DecisionReason: "fifo_green_express_overcommit"}
		}
		return workSelection{ProtectedOperationID: protectedOperationID, DecisionReason: "fifo_head_does_not_fit"}
	}
	return workSelection{SelectedOperationID: waiters[0].OperationID, ProtectedOperationID: protectedOperationID, DecisionReason: "fifo_head_fits"}
}

func (coordinator *WorkCoordinator) schedulingPolicy() string {
	if coordinator != nil && coordinator.SchedulingPolicy == WorkSchedulingPolicy {
		return WorkSchedulingPolicy
	}
	return WorkSchedulingPolicyFIFO
}

// expressLeasedWeight sums the weight of currently-leased express-class work —
// the cumulative drain ride-through budget input. Callers hold the state lock.
func expressLeasedWeight(leases []WorkLeaseRecord) int {
	total := 0
	for _, lease := range leases {
		if IsExpressClass(lease.Class) {
			total += lease.Weight
		}
	}
	return total
}

func (coordinator *WorkCoordinator) selectWaiter(waiters []WorkWaiterRecord, used, capacity int, now time.Time, green workGreenExpressWindow, expressLeased int) workSelection {
	if coordinator.schedulingPolicy() == WorkSchedulingPolicy {
		return selectWorkWaiter(waiters, used, capacity, now, green, expressLeased)
	}
	return selectFIFOWorkWaiter(waiters, used, capacity, now, green)
}

func materializeWorkProtections(waiters []WorkWaiterRecord, capacity int, now time.Time) {
	for index := range waiters {
		if waiters[index].ProtectedAt != nil {
			continue
		}
		protected, _ := workWaiterProtection(waiters[index], capacity, now)
		if protected {
			protectedAt := now
			waiters[index].ProtectedAt = &protectedAt
		}
	}
}

func (coordinator *WorkCoordinator) status(state workState, prunedLeases, prunedWaiters int) WorkStatus {
	limits := coordinator.limits()
	records := append([]WorkLeaseRecord{}, state.Leases...)
	sort.Slice(records, func(i, j int) bool {
		if records[i].StartedAt.Equal(records[j].StartedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].StartedAt.Before(records[j].StartedAt)
	})
	now := coordinator.now()
	leases := make([]WorkLeaseStatus, 0, len(records))
	used := 0
	for _, record := range records {
		used += record.Weight
		age := max(time.Duration(0), now.Sub(record.StartedAt))
		review := age >= WorkFiniteLeaseReviewAge
		reason := ""
		if review {
			reason = "finite lease exceeded 15m; verify this is not a resident service and use ndev dev for future launches"
		}
		leases = append(leases, WorkLeaseStatus{
			ID: record.ID, OperationID: record.OperationID, Class: record.Class, Weight: record.Weight,
			PID: record.PID, StartedAt: record.StartedAt, AgeMS: age.Milliseconds(), Review: review, ReviewReason: reason,
		})
	}
	waiters := make([]WorkWaiterStatus, 0, len(state.Waiters))
	pressureReservationCount := 0
	reservedWeight := 0
	overridePositions := workOverridePositions(&state)
	for index, record := range state.Waiters {
		protected, protectionReason := workWaiterProtection(record, limits.Capacity, now)
		overridePosition := overridePositions[record.OperationID]
		if record.OperationID == state.OverrideOperationID {
			protected = true
			protectionReason = "priority_override"
		} else if overridePosition > 0 {
			// A pinned tail entry is reserved by operator intent even though the
			// head is the only waiter the selector can admit right now.
			protected = true
			protectionReason = "priority_override_queued"
		}
		waiters = append(waiters, WorkWaiterStatus{
			OperationID: record.OperationID, Class: record.Class, Weight: record.Weight,
			PID: record.PID, QueuedAt: record.QueuedAt, HeartbeatAt: record.HeartbeatAt, Position: index + 1,
			WaitMS: max(int64(0), now.Sub(record.QueuedAt).Milliseconds()), BypassCount: record.BypassCount,
			Protected: protected, ProtectionReason: protectionReason,
			ReservationKind: record.ReservationKind, ReservedAt: record.ReservedAt,
			OverridePosition: overridePosition,
		})
		if record.ReservationKind == WorkReservationPressure {
			pressureReservationCount++
			reservedWeight += record.Weight
		}
	}
	holds := admissionHoldStatuses(state, now)
	longestHold := int64(0)
	for _, hold := range holds {
		longestHold = max(longestHold, hold.HeldForMS)
	}
	shadowSelection := workSelection{}
	candidatePolicy := ""
	if coordinator.schedulingPolicy() == WorkSchedulingPolicyFIFO {
		shadowSelection = selectWorkWaiter(state.Waiters, used, limits.Capacity, now, workGreenExpressWindow{}, 0)
		candidatePolicy = WorkSchedulingPolicy
	}
	// Status is a read projection: report the deterministic no-green selection
	// so displayed decisions never depend on the moment's monitor sample.
	selection := coordinator.selectStateWaiter(state, used, limits.Capacity, now, workGreenExpressWindow{})
	if state.lastSelection.SelectedOperationID != "" {
		selection = state.lastSelection
	}
	if candidatePolicy != "" && state.lastShadowSelection.SelectedOperationID != "" {
		shadowSelection = state.lastShadowSelection
	}
	return WorkStatus{
		SchemaVersion:              state.SchemaVersion,
		Capacity:                   limits.Capacity,
		Used:                       used,
		Available:                  max(0, limits.Capacity-used),
		Leases:                     leases,
		Waiters:                    waiters,
		QueueDepth:                 len(waiters),
		Pruned:                     prunedLeases,
		PrunedWaiters:              prunedWaiters,
		StatePath:                  coordinator.statePath(),
		SchedulingPolicy:           coordinator.schedulingPolicy(),
		SelectorSchemaVersion:      workSelectorSchemaVersion,
		SelectedOperationID:        selection.SelectedOperationID,
		ProtectedOperationID:       selection.ProtectedOperationID,
		DecisionReason:             selection.DecisionReason,
		BypassedCount:              len(selection.BypassedIndexes),
		CandidateSchedulingPolicy:  candidatePolicy,
		ShadowSelectedOperationID:  shadowSelection.SelectedOperationID,
		ShadowProtectedOperationID: shadowSelection.ProtectedOperationID,
		ShadowDecisionReason:       shadowSelection.DecisionReason,
		OverrideOperationID:        state.OverrideOperationID,
		OverrideRequestedAt:        state.OverrideRequestedAt,
		OverrideQueue:              append([]string(nil), state.OverrideQueue...),
		OverrideQueueDepth:         len(overridePositions),
		PressureReservationCount:   pressureReservationCount,
		ReservedWeight:             reservedWeight,
		AdmissionHolds:             holds,
		AdmissionHoldCount:         len(holds),
		PrunedAdmissionHolds:       state.prunedAdmissionHolds,
		LongestAdmissionHold:       longestHold,
		AdmissionLatch:             state.AdmissionLatch,
	}
}

const workStateLockFallback = 5 * time.Second

// workStateLockTimeout follows the caller deadline. Sample attribution uses a
// 500ms ctx; a hard 5s lock wait was turning that into a 5s wall stall.
func workStateLockTimeout(ctx context.Context) time.Duration {
	if ctx == nil {
		return workStateLockFallback
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return workStateLockFallback
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return time.Millisecond
	}
	if remaining < workStateLockFallback {
		return remaining
	}
	return workStateLockFallback
}

func (coordinator *WorkCoordinator) withState(ctx context.Context, mutate func(*workState) error) (WorkStatus, error) {
	if err := coordinator.validate(); err != nil {
		return WorkStatus{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	unlock, err := filelock.AcquireContext(ctx, coordinator.statePath(), workStateLockTimeout(ctx))
	if err != nil {
		return WorkStatus{}, fmt.Errorf("lock work lease state: %w", err)
	}
	defer unlock()
	state, err := coordinator.readState()
	if err != nil {
		return WorkStatus{}, err
	}
	prunedLeases, prunedWaiters, reconciled, err := coordinator.reconcile(&state)
	if err != nil {
		return WorkStatus{}, fmt.Errorf("validate work lease state: %w", err)
	}
	if mutate != nil {
		// A newer helper must not publish a schema the n-1 helper cannot read
		// while that helper still owns a lease. Below schema 6 that protection is
		// load-bearing: those shapes have no operation_id and no supervisor
		// identity, so writing a lease into them would genuinely lose ownership
		// attribution. Those mutations still fail closed.
		//
		// At schema 6 and above the differences are additive and loss-tolerant
		// (override queue, admission holds, shared latch), so refusing there cost
		// correctness nothing and cost availability everything: the upgrade window
		// needs leases and waiters simultaneously empty, which under continuous
		// parallel-agent work may never arrive. One 43-minute lease was observed
		// refusing every acquisition on the host — five of eight weighted slots
		// free, queue empty — until it finally exited. So from 6 up, accept the
		// mutation and pin the document to its current schema; writeState already
		// emits a compatible shape, and the version advances on its own once the
		// queue genuinely drains.
		legacyBlocking := state.legacyActive && (len(state.Leases) > 0 || len(state.Waiters) > 0)
		if legacyBlocking && state.SchemaVersion < workReservationMinimumSchema {
			if reconciled {
				if writeErr := coordinator.writeState(state); writeErr != nil {
					return coordinator.status(state, prunedLeases, prunedWaiters), writeErr
				}
			}
			return coordinator.status(state, prunedLeases, prunedWaiters), ErrWorkUpgradePending
		}
		legacyPinned := legacyBlocking
		if err := mutate(&state); err != nil {
			// Mutators validate before changing durable ownership. The only
			// expected error-path mutation is a queued waiter's heartbeat, plus
			// dead-owner cleanup from reconciliation; persist both so a long queue
			// remains truthful and does not rediscover stale records on every poll.
			if writeErr := coordinator.writeState(state); writeErr != nil {
				return coordinator.status(state, prunedLeases, prunedWaiters), errors.Join(err, writeErr)
			}
			return coordinator.status(state, prunedLeases, prunedWaiters), err
		}
		if !legacyPinned && state.SchemaVersion < workStateSchemaVersion {
			state.SchemaVersion = workStateSchemaVersion
		}
	}
	if reconciled || mutate != nil {
		if err := coordinator.writeState(state); err != nil {
			return WorkStatus{}, err
		}
	}
	return coordinator.status(state, prunedLeases, prunedWaiters), nil
}

func (coordinator *WorkCoordinator) Status(ctx context.Context) (WorkStatus, error) {
	return coordinator.withState(ctx, nil)
}

type WorkLease struct {
	coordinator *WorkCoordinator
	record      WorkLeaseRecord
	released    bool
}

func (lease *WorkLease) Record() WorkLeaseRecord {
	if lease == nil {
		return WorkLeaseRecord{}
	}
	return lease.record
}

// BindPID transfers crash-recovery ownership from the short-lived ndev wrapper
// to the leased child. If the wrapper is SIGKILLed, the lease then remains held
// for as long as the heavy command is actually alive instead of being pruned
// while orphaned compilation continues.
func (lease *WorkLease) BindPID(ctx context.Context, pid int) error {
	if lease == nil || lease.coordinator == nil || lease.released {
		return errors.New("work lease is not active")
	}
	if pid <= 0 {
		return errors.New("work lease PID must be positive")
	}
	ownerIdentity, err := lease.coordinator.identityForPID(pid)
	if err != nil {
		return err
	}
	found := false
	_, err = lease.coordinator.withState(ctx, func(state *workState) error {
		for index := range state.Leases {
			if state.Leases[index].ID == lease.record.ID {
				state.Leases[index].PID = pid
				state.Leases[index].OwnerIdentity = ownerIdentity
				found = true
				break
			}
		}
		if !found {
			return errors.New("work lease disappeared before child binding")
		}
		return nil
	})
	if err != nil {
		return err
	}
	lease.record.PID = pid
	lease.record.OwnerIdentity = ownerIdentity
	return nil
}

// ReserveForPressure converts a not-yet-started lease into a protected queue
// reservation without charging weighted capacity. The original queue time is
// retained so a host-pressure recheck cannot send the oldest operation to the
// back of the line.
func (lease *WorkLease) ReserveForPressure(ctx context.Context) (*WorkWaiter, WorkStatus, error) {
	if lease == nil || lease.coordinator == nil || lease.released {
		return nil, WorkStatus{}, errors.New("work lease is not active")
	}
	ownerIdentity, err := lease.coordinator.identityForPID(lease.coordinator.PID)
	if err != nil {
		return nil, WorkStatus{}, err
	}
	now := lease.coordinator.now()
	queuedAt := lease.record.QueuedAt
	if queuedAt.IsZero() {
		queuedAt = lease.record.StartedAt
	}
	reservedAt := now
	protectedAt := now
	waiterRecord := WorkWaiterRecord{
		OperationID: lease.record.OperationID, Class: lease.record.Class, Weight: lease.record.Weight,
		PID: lease.coordinator.PID, OwnerIdentity: ownerIdentity, QueuedAt: queuedAt, HeartbeatAt: now,
		ReservationKind: WorkReservationPressure, ReservedAt: &reservedAt, ProtectedAt: &protectedAt,
	}
	found := false
	status, err := lease.coordinator.withState(ctx, func(state *workState) error {
		// Refuse only when the persisted document genuinely cannot express a
		// non-consuming reservation, not merely because it trails the newest
		// schema. Gating on "behind current" meant one long-running legacy-era
		// lease refused every pressure reservation on the host for its entire
		// lifetime — observed as a 43-minute total stall with five of eight
		// weighted slots free and an empty queue — even though schema 6 already
		// carries reservation_kind and reserved_at faithfully.
		if state.SchemaVersion < workReservationMinimumSchema {
			return ErrWorkUpgradePending
		}
		kept := state.Leases[:0]
		for _, existing := range state.Leases {
			if existing.ID == lease.record.ID {
				if existing.OperationID != lease.record.OperationID {
					return errors.New("work lease operation changed before pressure reservation")
				}
				found = true
				continue
			}
			kept = append(kept, existing)
		}
		if !found {
			return errors.New("work lease disappeared before pressure reservation")
		}
		state.Leases = kept
		state.Waiters = append(state.Waiters, waiterRecord)
		sort.Slice(state.Waiters, func(i, j int) bool {
			if state.Waiters[i].QueuedAt.Equal(state.Waiters[j].QueuedAt) {
				return state.Waiters[i].OperationID < state.Waiters[j].OperationID
			}
			return state.Waiters[i].QueuedAt.Before(state.Waiters[j].QueuedAt)
		})
		return nil
	})
	if err != nil {
		return nil, status, err
	}
	lease.released = true
	return &WorkWaiter{coordinator: lease.coordinator, operationID: lease.record.OperationID, class: lease.record.Class}, status, nil
}

func (coordinator *WorkCoordinator) now() time.Time {
	now := time.Now
	if coordinator.Now != nil {
		now = coordinator.Now
	}
	return now().UTC()
}

func (coordinator *WorkCoordinator) newLeaseRecord(class WorkClass, operationID string) (WorkLeaseRecord, error) {
	if err := coordinator.validate(); err != nil {
		return WorkLeaseRecord{}, err
	}
	weight, err := coordinator.limits().Weight(class)
	if err != nil {
		return WorkLeaseRecord{}, err
	}
	ownerIdentity, err := coordinator.identityForPID(coordinator.PID)
	if err != nil {
		return WorkLeaseRecord{}, err
	}
	if !validPrivateID(operationID) {
		return WorkLeaseRecord{}, errors.New("work operation_id must be a 32-character lowercase hex identity")
	}
	leaseID, err := randomPrivateID()
	if err != nil {
		return WorkLeaseRecord{}, err
	}
	now := coordinator.now()
	return WorkLeaseRecord{
		ID: leaseID, OperationID: operationID, Class: class, Weight: weight,
		PID: coordinator.PID, OwnerIdentity: ownerIdentity,
		SupervisorPID: coordinator.PID, SupervisorIdentity: ownerIdentity, QueuedAt: now, StartedAt: now,
	}, nil
}

func (coordinator *WorkCoordinator) Acquire(ctx context.Context, class WorkClass) (*WorkLease, WorkStatus, error) {
	operationID, err := NewWorkOperationID()
	if err != nil {
		return nil, WorkStatus{}, err
	}
	return coordinator.AcquireOperation(ctx, class, operationID)
}

func (coordinator *WorkCoordinator) AcquireOperation(ctx context.Context, class WorkClass, operationID string) (*WorkLease, WorkStatus, error) {
	return coordinator.AcquireOperationWithCapacity(ctx, class, operationID, coordinator.limits().Capacity)
}

// AcquireOperationWithCapacity atomically applies a caller-selected effective
// ceiling inside the coordinator state lock. Warning-pressure callers use this
// to close the gap between observing host pressure and acquiring a lease.
func (coordinator *WorkCoordinator) AcquireOperationWithCapacity(ctx context.Context, class WorkClass, operationID string, effectiveCapacity int) (*WorkLease, WorkStatus, error) {
	record, err := coordinator.newLeaseRecord(class, operationID)
	if err != nil {
		return nil, WorkStatus{}, err
	}
	limits := coordinator.limits()
	effectiveCapacity = min(max(1, effectiveCapacity), limits.Capacity)
	green := coordinator.greenExpressWindow()
	if effectiveCapacity < limits.Capacity {
		green = workGreenExpressWindow{}
	}
	status, err := coordinator.withState(ctx, func(state *workState) error {
		if len(state.Waiters) > 0 {
			return fmt.Errorf("%w: operation=%s queued_operation=%s position=1", ErrWorkFairness, operationID, state.Waiters[0].OperationID)
		}
		used := 0
		for _, existing := range state.Leases {
			used += existing.Weight
		}
		if used+record.Weight > effectiveCapacity {
			if !(green.Active && IsExpressClass(class) && used+record.Weight <= effectiveCapacity+green.Overcommit) {
				return fmt.Errorf("%w: class=%s weight=%d used=%d capacity=%d", ErrWorkCapacity, class, record.Weight, used, effectiveCapacity)
			}
		}
		state.Leases = append(state.Leases, record)
		return nil
	})
	if err != nil {
		return nil, status, err
	}
	return &WorkLease{coordinator: coordinator, record: record}, status, nil
}

type WorkWaiter struct {
	coordinator *WorkCoordinator
	operationID string
	class       WorkClass
	cancelled   bool
}

func (coordinator *WorkCoordinator) RegisterWaiter(ctx context.Context, class WorkClass, operationID string) (*WorkWaiter, WorkStatus, error) {
	if err := coordinator.validate(); err != nil {
		return nil, WorkStatus{}, err
	}
	if !validPrivateID(operationID) {
		return nil, WorkStatus{}, errors.New("work operation_id must be a 32-character lowercase hex identity")
	}
	weight, err := coordinator.limits().Weight(class)
	if err != nil {
		return nil, WorkStatus{}, err
	}
	ownerIdentity, err := coordinator.identityForPID(coordinator.PID)
	if err != nil {
		return nil, WorkStatus{}, err
	}
	now := coordinator.now()
	record := WorkWaiterRecord{
		OperationID: operationID, Class: class, Weight: weight, PID: coordinator.PID,
		OwnerIdentity: ownerIdentity, QueuedAt: now, HeartbeatAt: now,
	}
	status, err := coordinator.withState(ctx, func(state *workState) error {
		for _, lease := range state.Leases {
			if lease.OperationID == operationID {
				return fmt.Errorf("work operation %s already owns a lease", operationID)
			}
		}
		for _, waiter := range state.Waiters {
			if waiter.OperationID == operationID {
				return fmt.Errorf("work operation %s is already queued", operationID)
			}
		}
		state.Waiters = append(state.Waiters, record)
		sort.Slice(state.Waiters, func(i, j int) bool {
			if state.Waiters[i].QueuedAt.Equal(state.Waiters[j].QueuedAt) {
				return state.Waiters[i].OperationID < state.Waiters[j].OperationID
			}
			return state.Waiters[i].QueuedAt.Before(state.Waiters[j].QueuedAt)
		})
		return nil
	})
	if err != nil {
		return nil, status, err
	}
	return &WorkWaiter{coordinator: coordinator, operationID: operationID, class: class}, status, nil
}

func (waiter *WorkWaiter) TryAcquire(ctx context.Context) (*WorkLease, WorkStatus, error) {
	if waiter == nil || waiter.coordinator == nil || waiter.cancelled {
		return nil, WorkStatus{}, errors.New("work waiter is not active")
	}
	return waiter.TryAcquireWithCapacity(ctx, waiter.coordinator.limits().Capacity)
}

// TryAcquireWithCapacity applies the effective ceiling in the same locked
// transaction that selects the waiter and materializes its lease.
func (waiter *WorkWaiter) TryAcquireWithCapacity(ctx context.Context, effectiveCapacity int) (*WorkLease, WorkStatus, error) {
	if waiter == nil || waiter.coordinator == nil || waiter.cancelled {
		return nil, WorkStatus{}, errors.New("work waiter is not active")
	}
	record, err := waiter.coordinator.newLeaseRecord(waiter.class, waiter.operationID)
	if err != nil {
		return nil, WorkStatus{}, err
	}
	limits := waiter.coordinator.limits()
	effectiveCapacity = min(max(1, effectiveCapacity), limits.Capacity)
	// Resolved outside the state lock: one bounded file read, and selection
	// itself stays pure.
	green := waiter.coordinator.greenExpressWindow()
	if effectiveCapacity < limits.Capacity {
		green = workGreenExpressWindow{}
	}
	status, err := waiter.coordinator.withState(ctx, func(state *workState) error {
		now := waiter.coordinator.now()
		position := -1
		for index := range state.Waiters {
			if state.Waiters[index].OperationID == waiter.operationID {
				position = index
				state.Waiters[index].HeartbeatAt = now
				break
			}
		}
		if position < 0 {
			return errors.New("work waiter disappeared before acquisition")
		}
		record.QueuedAt = state.Waiters[position].QueuedAt
		used := 0
		for _, lease := range state.Leases {
			used += lease.Weight
		}
		materializeWorkProtections(state.Waiters, limits.Capacity, now)
		selection := waiter.coordinator.selectStateWaiter(*state, used, effectiveCapacity, now, green)
		shadowSelection := workSelection{}
		if waiter.coordinator.schedulingPolicy() == WorkSchedulingPolicyFIFO {
			shadowSelection = selectWorkWaiter(state.Waiters, used, effectiveCapacity, now, workGreenExpressWindow{}, 0)
		}
		if selection.SelectedOperationID != waiter.operationID {
			if selection.ProtectedOperationID != "" {
				return fmt.Errorf("%w: operation=%s protected_operation=%s reason=%s", ErrWorkReservation, waiter.operationID, selection.ProtectedOperationID, selection.DecisionReason)
			}
			if selection.SelectedOperationID != "" {
				return fmt.Errorf("%w: operation=%s selected_operation=%s reason=%s", ErrWorkFairness, waiter.operationID, selection.SelectedOperationID, selection.DecisionReason)
			}
			return fmt.Errorf("%w: class=%s weight=%d used=%d capacity=%d", ErrWorkCapacity, waiter.class, record.Weight, used, effectiveCapacity)
		}
		for _, bypassedIndex := range selection.BypassedIndexes {
			state.Waiters[bypassedIndex].BypassCount++
			if state.Waiters[bypassedIndex].LastBypassedAt == nil {
				bypassedAt := now
				state.Waiters[bypassedIndex].LastBypassedAt = &bypassedAt
			}
			if state.Waiters[bypassedIndex].BypassCount >= workMaximumBypasses && state.Waiters[bypassedIndex].ProtectedAt == nil {
				protectedAt := now
				state.Waiters[bypassedIndex].ProtectedAt = &protectedAt
			}
		}
		state.lastSelection = selection
		state.lastShadowSelection = shadowSelection
		state.Waiters = append(state.Waiters[:position], state.Waiters[position+1:]...)
		// The pinned head just acquired; hand the reservation to the next live
		// entry in the operator sequence instead of dropping the whole request.
		advanceWorkOverride(state, now)
		state.Leases = append(state.Leases, record)
		return nil
	})
	if err != nil {
		return nil, status, err
	}
	waiter.cancelled = true
	return &WorkLease{coordinator: waiter.coordinator, record: record}, status, nil
}

func (waiter *WorkWaiter) Cancel(ctx context.Context) error {
	if waiter == nil || waiter.coordinator == nil || waiter.cancelled {
		return nil
	}
	_, err := waiter.coordinator.withState(ctx, func(state *workState) error {
		kept := state.Waiters[:0]
		for _, existing := range state.Waiters {
			if existing.OperationID != waiter.operationID {
				kept = append(kept, existing)
			}
		}
		state.Waiters = kept
		advanceWorkOverride(state, waiter.coordinator.now())
		return nil
	})
	if err == nil {
		waiter.cancelled = true
	}
	return err
}

type WorkWaitProgress func(status WorkStatus, blocker WorkBlocker)

func (coordinator *WorkCoordinator) WaitAcquireOperation(ctx context.Context, class WorkClass, operationID string, progress WorkWaitProgress) (*WorkLease, WorkStatus, error) {
	return coordinator.WaitAcquireOperationWithCapacity(ctx, class, operationID, coordinator.limits().Capacity, progress)
}

func (coordinator *WorkCoordinator) WaitAcquireOperationWithCapacity(ctx context.Context, class WorkClass, operationID string, effectiveCapacity int, progress WorkWaitProgress) (*WorkLease, WorkStatus, error) {
	return coordinator.waitAcquireOperation(ctx, class, operationID, false, effectiveCapacity, progress)
}

// WaitAcquirePriorityOperation registers the caller's own waiter and appends it
// to the pinned sequence before waiting. The priority request never skips host
// admission or weighted capacity and never displaces an operator-pinned entry.
func (coordinator *WorkCoordinator) WaitAcquirePriorityOperation(ctx context.Context, class WorkClass, operationID string, progress WorkWaitProgress) (*WorkLease, WorkStatus, error) {
	return coordinator.WaitAcquirePriorityOperationWithCapacity(ctx, class, operationID, coordinator.limits().Capacity, progress)
}

func (coordinator *WorkCoordinator) WaitAcquirePriorityOperationWithCapacity(ctx context.Context, class WorkClass, operationID string, effectiveCapacity int, progress WorkWaitProgress) (*WorkLease, WorkStatus, error) {
	return coordinator.waitAcquireOperation(ctx, class, operationID, true, effectiveCapacity, progress)
}

func (coordinator *WorkCoordinator) waitAcquireOperation(ctx context.Context, class WorkClass, operationID string, priority bool, effectiveCapacity int, progress WorkWaitProgress) (*WorkLease, WorkStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var waiter *WorkWaiter
	var status WorkStatus
	for {
		var err error
		waiter, status, err = coordinator.RegisterWaiter(ctx, class, operationID)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrWorkUpgradePending) {
			return nil, status, err
		}
		if progress != nil {
			progress(status, WorkBlockerUpgrade)
		}
		timer := time.NewTimer(workCapacityRetryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, status, fmt.Errorf("wait for legacy heavy-work state upgrade: %w", ctx.Err())
		case <-timer.C:
		}
	}
	if priority {
		priorityResult, priorityStatus, priorityErr := coordinator.PrioritizeWaiter(ctx, operationID)
		status = priorityStatus
		if priorityErr != nil {
			cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			cancelErr := waiter.Cancel(cancelCtx)
			cancel()
			return nil, status, errors.Join(priorityErr, cancelErr)
		}
		if priorityResult.OperationID != operationID {
			cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			cancelErr := waiter.Cancel(cancelCtx)
			cancel()
			return nil, status, errors.Join(errors.New("agent priority did not pin the requesting operation"), cancelErr)
		}
	}
	return waiter.WaitAcquireWithCapacity(ctx, effectiveCapacity, progress)
}

// WaitAcquire waits on an already-registered waiter. Pressure reservations use
// this path after their host gate clears so they preserve one durable queue
// identity and its original age.
func (waiter *WorkWaiter) WaitAcquire(ctx context.Context, progress WorkWaitProgress) (*WorkLease, WorkStatus, error) {
	if waiter == nil || waiter.coordinator == nil || waiter.cancelled {
		return nil, WorkStatus{}, errors.New("work waiter is not active")
	}
	return waiter.WaitAcquireWithCapacity(ctx, waiter.coordinator.limits().Capacity, progress)
}

func (waiter *WorkWaiter) WaitAcquireWithCapacity(ctx context.Context, effectiveCapacity int, progress WorkWaitProgress) (*WorkLease, WorkStatus, error) {
	if waiter == nil || waiter.coordinator == nil || waiter.cancelled {
		return nil, WorkStatus{}, errors.New("work waiter is not active")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var status WorkStatus
	cancelWaiter := func() error {
		cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return waiter.Cancel(cancelCtx)
	}
	for {
		lease, currentStatus, err := waiter.TryAcquireWithCapacity(ctx, effectiveCapacity)
		status = currentStatus
		if err == nil {
			return lease, status, err
		}
		if !errors.Is(err, ErrWorkCapacity) && !errors.Is(err, ErrWorkFairness) && !errors.Is(err, ErrWorkReservation) {
			return nil, status, errors.Join(err, cancelWaiter())
		}
		if progress != nil {
			blocker := WorkBlockerCapacity
			if errors.Is(err, ErrWorkFairness) {
				blocker = WorkBlockerFairness
			}
			if errors.Is(err, ErrWorkReservation) {
				blocker = WorkBlockerReservation
			}
			progress(status, blocker)
		}
		timer := time.NewTimer(workCapacityRetryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			cancelErr := cancelWaiter()
			return nil, status, errors.Join(fmt.Errorf("wait for heavy-work queue: %w", ctx.Err()), cancelErr)
		case <-timer.C:
		}
	}
}

func (coordinator *WorkCoordinator) WaitAcquire(ctx context.Context, class WorkClass) (*WorkLease, WorkStatus, error) {
	operationID, err := NewWorkOperationID()
	if err != nil {
		return nil, WorkStatus{}, err
	}
	return coordinator.WaitAcquireOperation(ctx, class, operationID, nil)
}

func (lease *WorkLease) Release(ctx context.Context) error {
	if lease == nil || lease.coordinator == nil || lease.released {
		return nil
	}
	_, err := lease.coordinator.withState(ctx, func(state *workState) error {
		kept := state.Leases[:0]
		for _, existing := range state.Leases {
			if existing.ID != lease.record.ID {
				kept = append(kept, existing)
			}
		}
		state.Leases = kept
		return nil
	})
	if err == nil {
		lease.released = true
	}
	return err
}
