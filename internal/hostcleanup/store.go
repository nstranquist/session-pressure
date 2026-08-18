package hostcleanup

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/internal/atomicfile"
	"github.com/nstranquist/session-pressure/internal/filelock"
	"github.com/nstranquist/session-pressure/sessionpressure"
)

func PolicyPath(dir string) string  { return filepath.Join(dir, "cleanup-policy.json") }
func ClaimsDir(dir string) string   { return filepath.Join(dir, "resource-claims") }
func ActionsPath(dir string) string { return filepath.Join(dir, "resource-cleanup-actions.jsonl") }

func claimsLockPath(dir string) string { return filepath.Join(dir, "resource-claims-state") }

func LoadPolicy(dir string) (Policy, bool, error) {
	return loadPolicyFile(PolicyPath(dir))
}

func loadPolicyFile(path string) (Policy, bool, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultPolicy(), false, nil
		}
		return Policy{}, false, err
	}
	var policy Policy
	if err := json.Unmarshal(body, &policy); err != nil {
		return Policy{}, true, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, true, fmt.Errorf("validate %s: %w", path, err)
	}
	return policy, true, nil
}

func SavePolicy(dir string, policy Policy) error {
	if policy.Enabled && !policy.Enforce && policy.ObservationStartedAt.IsZero() {
		policy.ObservationStartedAt = time.Now().UTC()
	}
	if err := policy.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	unlock, err := filelock.Acquire(PolicyPath(dir), 5*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	return writePolicyFile(PolicyPath(dir), policy)
}

func writePolicyFile(path string, policy Policy) error {
	body, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return atomicfile.WriteFile(path, body, 0o600)
}

// ReconcileAutoGraduation performs the durable, explicitly scheduled rollout
// transition under the same policy lock used by operator mutations. It can
// only enable the reviewed process stage after its full observation window,
// then advances browser, dev-session, and Docker one additional weekly soak at
// a time. The caller returns after a transition so graduation and reclaim can
// never happen in the same control pass.
func ReconcileAutoGraduation(dir string, now time.Time) (policy Policy, persisted, graduated bool, err error) {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Policy{}, false, false, err
	}
	path := PolicyPath(dir)
	unlock, err := filelock.Acquire(path, 5*time.Second)
	if err != nil {
		return Policy{}, false, false, err
	}
	defer unlock()
	policy, persisted, err = loadPolicyFile(path)
	if err != nil || !persisted || !policy.Enabled || !policy.AutoGraduationDue(now) {
		return policy, persisted, false, err
	}
	switch {
	case !policy.Enforce:
		policy.Enforce = true
		policy.AutoGraduateProcessOnly = false
		policy.ProcessEnabled = true
		policy.BrowserEnabled = false
		policy.DevSessionEnabled = false
		policy.DockerWorkspaceEnabled = false
	case !policy.BrowserEnabled:
		policy.BrowserEnabled = true
	case !policy.DevSessionEnabled:
		policy.DevSessionEnabled = true
	case !policy.DockerWorkspaceEnabled:
		policy.DockerWorkspaceEnabled = true
		policy.AutoGraduateNative = false
	default:
		return policy, persisted, false, nil
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, persisted, false, err
	}
	if err := policy.ValidateEnforcement(now); err != nil {
		return Policy{}, persisted, false, err
	}
	if err := writePolicyFile(path, policy); err != nil {
		return Policy{}, persisted, false, err
	}
	return policy, persisted, true, nil
}

type ClaimStore struct {
	Dir string
	Now func() time.Time
}

func NewClaimStore(dir string) *ClaimStore {
	return &ClaimStore{Dir: dir, Now: time.Now}
}

func (store *ClaimStore) claimPath(id string) string {
	return filepath.Join(ClaimsDir(store.Dir), id+".json")
}

func (store *ClaimStore) Acquire(kind ResourceKind, resourceID, owner string, ttl time.Duration, cleanupOnStale bool, rootPID int, note string) (Claim, error) {
	if ttl < time.Minute || ttl > 7*24*time.Hour {
		return Claim{}, fmt.Errorf("claim TTL must be between 1m and 168h")
	}
	now := store.Now().UTC()
	id, err := randomID("claim")
	if err != nil {
		return Claim{}, err
	}
	claim := Claim{
		SchemaVersion: SchemaVersion, ID: id, ResourceKind: kind, ResourceID: resourceID,
		Owner: strings.TrimSpace(owner), AcquiredAt: now, HeartbeatAt: now,
		ExpiresAt: now.Add(ttl), TTLSeconds: int(ttl / time.Second),
		CleanupOnStale: cleanupOnStale, RootPID: rootPID, Note: strings.TrimSpace(note),
	}
	if kind == ResourceProcess {
		identity, identityErr := sessionpressure.CaptureProcessIdentity(rootPID)
		if identityErr != nil {
			return Claim{}, identityErr
		}
		claim.ProcessIdentity = identity
	}
	if err := claim.Validate(now); err != nil {
		return Claim{}, err
	}
	if err := os.MkdirAll(ClaimsDir(store.Dir), 0o700); err != nil {
		return Claim{}, err
	}
	unlock, err := store.lock()
	if err != nil {
		return Claim{}, err
	}
	defer unlock()
	path := store.claimPath(claim.ID)
	body, err := json.MarshalIndent(claim, "", "  ")
	if err != nil {
		return Claim{}, err
	}
	body = append(body, '\n')
	if err := atomicfile.WriteFile(path, body, 0o600); err != nil {
		return Claim{}, err
	}
	return claim, nil
}

func (store *ClaimStore) Heartbeat(id string) (Claim, error) {
	if !safeToken(id, 128) {
		return Claim{}, fmt.Errorf("invalid claim id")
	}
	path := store.claimPath(id)
	unlockState, err := store.lock()
	if err != nil {
		return Claim{}, err
	}
	defer unlockState()
	unlock, err := filelock.Acquire(path, 5*time.Second)
	if err != nil {
		return Claim{}, err
	}
	defer unlock()
	claim, err := readClaim(path, store.Now().UTC())
	if err != nil {
		return Claim{}, err
	}
	now := store.Now().UTC()
	claim.HeartbeatAt = now
	claim.ExpiresAt = now.Add(time.Duration(claim.TTLSeconds) * time.Second)
	body, err := json.MarshalIndent(claim, "", "  ")
	if err != nil {
		return Claim{}, err
	}
	body = append(body, '\n')
	if err := atomicfile.WriteFile(path, body, 0o600); err != nil {
		return Claim{}, err
	}
	return claim, nil
}

func (store *ClaimStore) Release(id string) error {
	if !safeToken(id, 128) {
		return fmt.Errorf("invalid claim id")
	}
	path := store.claimPath(id)
	unlockState, err := store.lock()
	if err != nil {
		return err
	}
	defer unlockState()
	unlock, err := filelock.Acquire(path, 5*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// List fails closed on every malformed entry. Silently skipping one corrupt
// protection claim could turn storage damage into destructive authority.
func (store *ClaimStore) List() ([]ClaimView, error) {
	unlock, err := store.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	return store.listUnlocked()
}

func (store *ClaimStore) listUnlocked() ([]ClaimView, error) {
	dir := ClaimsDir(store.Dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ClaimView{}, nil
		}
		return nil, err
	}
	now := store.Now().UTC()
	claims := make([]ClaimView, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		claim, err := readClaim(filepath.Join(dir, entry.Name()), now)
		if err != nil {
			return nil, err
		}
		claims = append(claims, claim.View(now))
	}
	sort.Slice(claims, func(i, j int) bool { return claims[i].ID < claims[j].ID })
	return claims, nil
}

func (store *ClaimStore) lock() (func(), error) {
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		return nil, err
	}
	return filelock.Acquire(claimsLockPath(store.Dir), 5*time.Second)
}

func readClaim(path string, now time.Time) (Claim, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Claim{}, err
	}
	var claim Claim
	if err := json.Unmarshal(body, &claim); err != nil {
		return Claim{}, fmt.Errorf("parse claim %s: %w", path, err)
	}
	if err := claim.Validate(now); err != nil {
		return Claim{}, fmt.Errorf("validate claim %s: %w", path, err)
	}
	if filepath.Base(path) != claim.ID+".json" {
		return Claim{}, fmt.Errorf("claim %s id does not match filename", path)
	}
	return claim, nil
}

func appendAction(dir string, action Action) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := ActionsPath(dir)
	unlock, err := filelock.Acquire(path, 5*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	body, err := json.Marshal(action)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func ReadActions(dir string, limit int) ([]Action, error) {
	path := ActionsPath(dir)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Action{}, nil
		}
		return nil, err
	}
	defer file.Close()
	rows := make([]Action, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var action Action
		if err := json.Unmarshal(scanner.Bytes(), &action); err != nil {
			return nil, fmt.Errorf("parse cleanup action ledger: %w", err)
		}
		if action.SchemaVersion != SchemaVersion || action.ID == "" || action.Timestamp.IsZero() {
			return nil, fmt.Errorf("cleanup action ledger contains an invalid row")
		}
		rows = append(rows, action)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}
	return rows, nil
}

func randomID(prefix string) (string, error) {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(bytes[:]), nil
}
