package gaterunner

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nstranquist/session-pressure/runnerfleet"
)

// Runner capacity is the "are we using the fleet well, and is anything about
// to limit it" view. It reads each inventory host once over SSH with a fixed,
// read-only probe: the runner's own 15-second host_samples (bucketed on the
// host), a jobs.log rollup, and kernel counters for memory, disk and network.
// NIC counters are recorded locally so bandwidth history survives restarts.
// Nothing here selects, binds, or moves work.

const CapacitySchemaVersion = 1

// Thresholds are diagnostics, not gates. They decide which alerts are raised.
const (
	cpuSpikePct           = 85.0
	iowaitHighPct         = 10.0
	memoryPressureRatio   = 0.10
	diskWarnPct           = 80.0
	diskCriticalPct       = 90.0
	loadHighPerCPU        = 1.5
	linkWarnRatio         = 0.70
	allowanceWarnRatio    = 0.70
	allowanceCritRatio    = 0.90
	underusedCPUPct       = 5.0
	underusedHeavyPct     = 10.0
	jobFailureWarnRatio   = 0.20
	jobFailureMinimumJobs = 10
	daysPerMonth          = 30.44
	maxUseRows            = 8
	probeOutputLimit      = 1 << 20
)

// CapacityWindow is a closed vocabulary. host_samples keeps 14 days.
type CapacityWindow struct {
	Name   string
	Span   time.Duration
	Bucket time.Duration
}

var capacityWindows = []CapacityWindow{
	{"6h", 6 * time.Hour, 5 * time.Minute},
	{"24h", 24 * time.Hour, 15 * time.Minute},
	{"7d", 7 * 24 * time.Hour, time.Hour},
	{"14d", 14 * 24 * time.Hour, 2 * time.Hour},
}

func ParseCapacityWindow(name string) (CapacityWindow, error) {
	for _, w := range capacityWindows {
		if w.Name == name {
			return w, nil
		}
	}
	names := make([]string, len(capacityWindows))
	for i, w := range capacityWindows {
		names[i] = w.Name
	}
	return CapacityWindow{}, fmt.Errorf("--window must be one of %s", strings.Join(names, ", "))
}

type CapacityReport struct {
	SchemaVersion int             `json:"schema_version"`
	CapturedAt    time.Time       `json:"captured_at"`
	Window        string          `json:"window"`
	Since         time.Time       `json:"since"`
	BucketSeconds int64           `json:"bucket_seconds"`
	Generation    string          `json:"generation,omitempty"`
	Hosts         []HostCapacity  `json:"hosts"`
	Alerts        []CapacityAlert `json:"alerts"`
	HistoryError  string          `json:"history_error,omitempty"`
	Error         string          `json:"error,omitempty"`
}

type HostCapacity struct {
	Host           string          `json:"host"`
	State          string          `json:"state"`
	Provider       string          `json:"provider"`
	Platform       string          `json:"platform"`
	HeavySlots     int             `json:"heavy_slots"`
	LightSlots     int             `json:"light_slots"`
	Reachable      bool            `json:"reachable"`
	Error          string          `json:"error,omitempty"`
	CPUs           int             `json:"cpus"`
	UptimeSeconds  float64         `json:"uptime_seconds"`
	Load1          float64         `json:"load1"`
	Load5          float64         `json:"load5"`
	Load15         float64         `json:"load15"`
	MemTotalBytes  uint64          `json:"mem_total_bytes"`
	MemAvailBytes  uint64          `json:"mem_available_bytes"`
	Disk           *DiskUse        `json:"disk,omitempty"`
	Network        *NetworkUse     `json:"network,omitempty"`
	SamplesPresent bool            `json:"samples_present"`
	Usage          UsageSummary    `json:"usage"`
	Buckets        []UsageBucket   `json:"buckets"`
	JobHours       []JobBucket     `json:"job_hours"`
	TopCallers     []UseRow        `json:"top_callers"`
	TopRepos       []UseRow        `json:"top_repos"`
	Alerts         []CapacityAlert `json:"-"`
}

type DiskUse struct {
	Mount      string  `json:"mount"`
	TotalBytes uint64  `json:"total_bytes"`
	UsedBytes  uint64  `json:"used_bytes"`
	AvailBytes uint64  `json:"available_bytes"`
	UsedPct    float64 `json:"used_pct"`
}

type NetworkUse struct {
	Interface             string           `json:"interface"`
	LinkMbps              *int             `json:"link_mbps,omitempty"`
	RxBytesSinceBoot      uint64           `json:"rx_bytes_since_boot"`
	TxBytesSinceBoot      uint64           `json:"tx_bytes_since_boot"`
	TailnetRxBytes        *uint64          `json:"tailnet_rx_bytes,omitempty"`
	TailnetTxBytes        *uint64          `json:"tailnet_tx_bytes,omitempty"`
	RxBytesPerDay         float64          `json:"rx_bytes_per_day"`
	TxBytesPerDay         float64          `json:"tx_bytes_per_day"`
	Allowance             TrafficAllowance `json:"allowance"`
	ProjectedMonthBytes   float64          `json:"projected_month_bytes"`
	ProjectedAllowancePct *float64         `json:"projected_allowance_pct,omitempty"`
	PeakRxBps             *float64         `json:"peak_rx_bps,omitempty"`
	PeakTxBps             *float64         `json:"peak_tx_bps,omitempty"`
	Series                []NetPoint       `json:"series"`
	SeriesCoverageSeconds int64            `json:"series_coverage_seconds"`

	bootID string
}

// TrafficAllowance records the provider's published traffic terms, not a
// measured quota. Kind is unmetered, monthly_included, or unknown.
type TrafficAllowance struct {
	Kind             string  `json:"kind"`
	Basis            string  `json:"basis,omitempty"`
	IncludedPerMonth *uint64 `json:"included_bytes_per_month,omitempty"`
	Note             string  `json:"note"`
}

type NetPoint struct {
	At    time.Time `json:"at"`
	RxBps float64   `json:"rx_bps"`
	TxBps float64   `json:"tx_bps"`
}

type UsageBucket struct {
	At              time.Time `json:"at"`
	Samples         int       `json:"samples"`
	CPUAvgPct       float64   `json:"cpu_avg_pct"`
	CPUPeakPct      float64   `json:"cpu_peak_pct"`
	IOWaitAvgPct    float64   `json:"iowait_avg_pct"`
	MemAvailMin     uint64    `json:"mem_available_min_bytes"`
	HeavyAvg        float64   `json:"heavy_avg"`
	HeavyMax        int       `json:"heavy_max"`
	LightMax        int       `json:"light_max"`
	SaturatedSecond float64   `json:"saturated_seconds"`

	saturatedSamples int
}

type UsageSummary struct {
	Samples           int      `json:"samples"`
	CPUAvgPct         *float64 `json:"cpu_avg_pct,omitempty"`
	CPUPeakPct        *float64 `json:"cpu_peak_pct,omitempty"`
	IOWaitAvgPct      *float64 `json:"iowait_avg_pct,omitempty"`
	MemAvailMinBytes  *uint64  `json:"mem_available_min_bytes,omitempty"`
	HeavyOccupancyPct *float64 `json:"heavy_occupancy_pct,omitempty"`
	HeavyPeak         *int     `json:"heavy_peak,omitempty"`
	SaturatedSeconds  float64  `json:"saturated_seconds"`
	Jobs              int      `json:"jobs"`
	FailedJobs        int      `json:"failed_jobs"`
	SetupFailures     int      `json:"setup_failures"`
	BusySeconds       int64    `json:"busy_seconds"`
}

type JobBucket struct {
	At          time.Time `json:"at"`
	Jobs        int       `json:"jobs"`
	Failed      int       `json:"failed"`
	Setup       int       `json:"setup"`
	BusySeconds int64     `json:"busy_seconds"`
}

type UseRow struct {
	Name        string `json:"name"`
	Jobs        int    `json:"jobs"`
	BusySeconds int64  `json:"busy_seconds"`
}

// CapacityAlert severities: critical, warning, info.
type CapacityAlert struct {
	Severity string     `json:"severity"`
	Code     string     `json:"code"`
	Host     string     `json:"host"`
	Title    string     `json:"title"`
	Detail   string     `json:"detail"`
	Since    *time.Time `json:"since,omitempty"`
	Until    *time.Time `json:"until,omitempty"`
}

type capacityProbe func(ctx context.Context, host runnerfleet.Host, script string) ([]byte, error)

func sshCapacityProbe(ctx context.Context, host runnerfleet.Host, script string) ([]byte, error) {
	bound, err := WithTarget(ctx, host.Name, host.User)
	if err != nil {
		return nil, err
	}
	return runSSH(bound, script, probeOutputLimit)
}

// Capacity reads every inventory host in parallel. dataDir holds the local
// NIC-counter history; an unusable store degrades to since-boot averages.
func Capacity(ctx context.Context, home, dataDir string, window CapacityWindow) CapacityReport {
	return capacityWith(ctx, home, dataDir, window, time.Now().UTC(), sshCapacityProbe)
}

func capacityWith(ctx context.Context, home, dataDir string, window CapacityWindow, now time.Time, probe capacityProbe) CapacityReport {
	r := CapacityReport{SchemaVersion: CapacitySchemaVersion, CapturedAt: now, Window: window.Name, Since: now.Add(-window.Span), BucketSeconds: int64(window.Bucket / time.Second), Hosts: []HostCapacity{}, Alerts: []CapacityAlert{}}
	path, _ := ctx.Value(fleetKey{}).(string)
	if path == "" {
		path = filepath.Join(home, ".nicos-dev", "gaterunner-fleet", "v1", "fleet.json")
	}
	c, _, err := runnerfleet.Load(path)
	if err != nil {
		if os.IsNotExist(err) {
			r.Error = "runner_fleet_inventory_missing"
		} else {
			r.Error = "runner_fleet_inventory_invalid"
		}
		return r
	}
	r.Generation = c.Generation
	store, storeErr := openCapacityStore(dataDir)
	if storeErr != nil {
		r.HistoryError = "network_history_unavailable"
	} else {
		defer store.Close()
	}
	r.Hosts = make([]HostCapacity, len(c.Hosts))
	var wg sync.WaitGroup
	for i, h := range c.Hosts {
		wg.Add(1)
		go func(i int, h runnerfleet.Host) {
			defer wg.Done()
			hc := HostCapacity{Host: h.Name, State: h.State, Provider: h.Provider, Platform: h.Platform, HeavySlots: h.HeavySlots, LightSlots: h.LightSlots, Buckets: []UsageBucket{}, JobHours: []JobBucket{}, TopCallers: []UseRow{}, TopRepos: []UseRow{}}
			raw, err := probe(ctx, h, capacityScript(r.Since, window.Bucket, h.HeavySlots))
			if err != nil {
				hc.Error = "runner_capacity_" + sshFailureKind(err)
			} else if p, err := parseCapacityProbe(string(raw), window.Bucket); err != nil {
				hc.Error = "runner_capacity_payload_invalid"
			} else {
				applyProbe(&hc, p, window.Bucket, now)
			}
			r.Hosts[i] = hc
		}(i, h)
	}
	wg.Wait()
	for i := range r.Hosts {
		h := &r.Hosts[i]
		if h.Network != nil && store != nil {
			if err := store.record(h.Host, now, h.Network); err != nil {
				r.HistoryError = "network_history_write_failed"
			}
			series, coverage, err := store.series(h.Host, h.Network.Interface, r.Since)
			if err != nil {
				r.HistoryError = "network_history_read_failed"
			} else {
				h.Network.Series = series
				h.Network.SeriesCoverageSeconds = coverage
				for _, p := range series {
					rx, tx := p.RxBps, p.TxBps
					if h.Network.PeakRxBps == nil || rx > *h.Network.PeakRxBps {
						h.Network.PeakRxBps = &rx
					}
					if h.Network.PeakTxBps == nil || tx > *h.Network.PeakTxBps {
						h.Network.PeakTxBps = &tx
					}
				}
			}
		}
		r.Alerts = append(r.Alerts, hostAlerts(*h, window)...)
	}
	if store != nil {
		_ = store.prune(now)
	}
	sortAlerts(r.Alerts)
	return r
}

// capacityScript is fixed text plus two integers and one timestamp computed
// here; no inventory or user value reaches the remote shell.
func capacityScript(since time.Time, bucket time.Duration, heavySlots int) string {
	if heavySlots < 1 {
		heavySlots = 1
	}
	cutoff := since.Unix()
	b := int64(bucket / time.Second)
	iso := since.UTC().Format("2006-01-02T15:04:05Z")
	return fmt.Sprintf(`echo __META__; nproc; cat /proc/uptime; cat /proc/loadavg; cat /proc/sys/kernel/random/boot_id
echo __MEM__; grep -E '^(MemTotal|MemAvailable):' /proc/meminfo
echo __DISK__; df -PB1 /srv/build 2>/dev/null || df -PB1 /
echo __ROUTE__; ip -o route show default 2>/dev/null
echo __NET__; tail -n +3 /proc/net/dev
echo __LINK__; for i in /sys/class/net/*; do printf '%%s %%s\n' "${i##*/}" "$(cat "$i/speed" 2>/dev/null)"; done
echo __SAMPLES__; db=/srv/build/scheduler/queue.db; if command -v sqlite3 >/dev/null 2>&1 && [ -r "$db" ] && sqlite3 -readonly "$db" "SELECT 1 FROM sqlite_master WHERE name='host_samples'" 2>/dev/null | grep -q 1; then echo PRESENT; sqlite3 -readonly -separator ' ' "$db" "SELECT (at/%[2]d)*%[2]d,count(*),avg(cpu_busy_ppm),max(cpu_busy_ppm),avg(iowait_ppm),min(mem_available),avg(heavy),max(heavy),max(light),max(cpus),sum(heavy>=%[4]d) FROM host_samples WHERE at>=%[1]d GROUP BY 1 ORDER BY 1"; else echo ABSENT; fi
echo __JOBS__; f=/srv/build/logs/jobs.log; if [ -r "$f" ]; then tail -n 50000 "$f" | awk -v c=%[3]s '$1>=c{h=substr($1,1,13);cl="-";rp="-";ex="?";d=0;for(i=2;i<=NF;i++){if($i~/^caller=/)cl=substr($i,8);else if($i~/^repo=/)rp=substr($i,6);else if($i~/^exit=/)ex=substr($i,6);else if($i~/^dur=/)d=substr($i,5)+0;else if($i~/^cmd=/)break};n[h]++;b[h]+=d;if(ex=="SETUP")s[h]++;else if(ex!="0")f[h]++;cn[cl]++;cb[cl]+=d;rn[rp]++;rb[rp]+=d}END{for(k in n)print "H",k,n[k],f[k]+0,s[k]+0,b[k];for(k in cn)print "C",k,cn[k],cb[k];for(k in rn)print "R",k,rn[k],rb[k]}'; fi
echo __END__
`, cutoff, b, iso, heavySlots)
}

type ifaceCounters struct{ rx, tx uint64 }

type capacityProbeData struct {
	cpus           int
	uptime         float64
	load           [3]float64
	bootID         string
	memTotal       uint64
	memAvail       uint64
	disk           *DiskUse
	defaultIface   string
	ifaces         map[string]ifaceCounters
	links          map[string]int
	samplesPresent bool
	buckets        []UsageBucket
	jobHours       []JobBucket
	callers        []UseRow
	repos          []UseRow
}

func parseCapacityProbe(raw string, bucket time.Duration) (capacityProbeData, error) {
	sections := map[string][]string{}
	current := ""
	ended := false
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "__") && strings.HasSuffix(trimmed, "__") && len(trimmed) > 4 {
			current = strings.Trim(trimmed, "_")
			if current == "END" {
				ended = true
				break
			}
			sections[current] = []string{}
			continue
		}
		if current != "" && trimmed != "" {
			sections[current] = append(sections[current], line)
		}
	}
	if !ended {
		return capacityProbeData{}, errors.New("capacity probe truncated")
	}
	p := capacityProbeData{ifaces: map[string]ifaceCounters{}, links: map[string]int{}}
	meta := sections["META"]
	if len(meta) < 3 {
		return p, errors.New("capacity probe meta missing")
	}
	n, err := strconv.Atoi(strings.TrimSpace(meta[0]))
	if err != nil || n <= 0 {
		return p, errors.New("capacity probe cpu count invalid")
	}
	p.cpus = n
	if f := strings.Fields(meta[1]); len(f) >= 1 {
		p.uptime, _ = strconv.ParseFloat(f[0], 64)
	}
	if f := strings.Fields(meta[2]); len(f) >= 3 {
		for i := 0; i < 3; i++ {
			v, err := strconv.ParseFloat(f[i], 64)
			if err != nil || v < 0 || math.IsInf(v, 0) || math.IsNaN(v) {
				return p, errors.New("capacity probe load invalid")
			}
			p.load[i] = v
		}
	} else {
		return p, errors.New("capacity probe load missing")
	}
	if len(meta) >= 4 {
		p.bootID = strings.TrimSpace(meta[3])
	}
	for _, line := range sections["MEM"] {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		kb, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			p.memTotal = kb * 1024
		case "MemAvailable:":
			p.memAvail = kb * 1024
		}
	}
	if p.memTotal == 0 || p.memAvail > p.memTotal {
		return p, errors.New("capacity probe memory invalid")
	}
	for _, line := range sections["DISK"] {
		f := strings.Fields(line)
		if len(f) < 6 || f[0] == "Filesystem" {
			continue
		}
		total, e1 := strconv.ParseUint(f[1], 10, 64)
		used, e2 := strconv.ParseUint(f[2], 10, 64)
		avail, e3 := strconv.ParseUint(f[3], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || total == 0 {
			continue
		}
		// df's Capacity column rounds up over used+available, not total.
		pct := 0.0
		if used+avail > 0 {
			pct = round1(float64(used) / float64(used+avail) * 100)
		}
		p.disk = &DiskUse{Mount: f[5], TotalBytes: total, UsedBytes: used, AvailBytes: avail, UsedPct: pct}
	}
	for _, line := range sections["ROUTE"] {
		f := strings.Fields(line)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "dev" && p.defaultIface == "" {
				p.defaultIface = f[i+1]
			}
		}
	}
	for _, line := range sections["NET"] {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, e1 := strconv.ParseUint(f[0], 10, 64)
		tx, e2 := strconv.ParseUint(f[8], 10, 64)
		if e1 == nil && e2 == nil {
			p.ifaces[strings.TrimSpace(name)] = ifaceCounters{rx, tx}
		}
	}
	for _, line := range sections["LINK"] {
		f := strings.Fields(line)
		if len(f) == 2 {
			if v, err := strconv.Atoi(f[1]); err == nil && v > 0 {
				p.links[f[0]] = v
			}
		}
	}
	samples := sections["SAMPLES"]
	if len(samples) > 0 && strings.TrimSpace(samples[0]) == "PRESENT" {
		p.samplesPresent = true
		for _, line := range samples[1:] {
			if b, ok := parseUsageBucket(line, bucket); ok {
				p.buckets = append(p.buckets, b)
			}
		}
	}
	for _, line := range sections["JOBS"] {
		f := strings.Fields(line)
		switch {
		case len(f) == 6 && f[0] == "H":
			at, err := time.Parse("2006-01-02T15", f[1])
			if err != nil {
				continue
			}
			jobs, _ := strconv.Atoi(f[2])
			failed, _ := strconv.Atoi(f[3])
			setup, _ := strconv.Atoi(f[4])
			busy, _ := strconv.ParseInt(f[5], 10, 64)
			p.jobHours = append(p.jobHours, JobBucket{At: at.UTC(), Jobs: jobs, Failed: failed, Setup: setup, BusySeconds: busy})
		case len(f) == 4 && (f[0] == "C" || f[0] == "R"):
			jobs, _ := strconv.Atoi(f[2])
			busy, _ := strconv.ParseInt(f[3], 10, 64)
			row := UseRow{Name: f[1], Jobs: jobs, BusySeconds: busy}
			if f[0] == "C" {
				p.callers = append(p.callers, row)
			} else {
				p.repos = append(p.repos, row)
			}
		}
	}
	sort.Slice(p.jobHours, func(i, j int) bool { return p.jobHours[i].At.Before(p.jobHours[j].At) })
	return p, nil
}

func parseUsageBucket(line string, bucket time.Duration) (UsageBucket, bool) {
	f := strings.Fields(line)
	if len(f) < 11 {
		return UsageBucket{}, false
	}
	nums := make([]float64, len(f))
	for i, s := range f {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return UsageBucket{}, false
		}
		nums[i] = v
	}
	if nums[1] <= 0 {
		return UsageBucket{}, false
	}
	b := UsageBucket{
		At:           time.Unix(int64(nums[0]), 0).UTC(),
		Samples:      int(nums[1]),
		CPUAvgPct:    round1(nums[2] / 10000),
		CPUPeakPct:   round1(nums[3] / 10000),
		IOWaitAvgPct: round1(nums[4] / 10000),
		MemAvailMin:  uint64(nums[5]),
		HeavyAvg:     round2(nums[6]),
		HeavyMax:     int(nums[7]),
		LightMax:     int(nums[8]),

		saturatedSamples: int(nums[10]),
	}
	return b, true
}

func applyProbe(h *HostCapacity, p capacityProbeData, bucket time.Duration, now time.Time) {
	h.Reachable = true
	h.CPUs, h.UptimeSeconds = p.cpus, p.uptime
	h.Load1, h.Load5, h.Load15 = p.load[0], p.load[1], p.load[2]
	h.MemTotalBytes, h.MemAvailBytes = p.memTotal, p.memAvail
	h.Disk = p.disk
	h.SamplesPresent = p.samplesPresent
	h.Buckets = nonNil(p.buckets)
	h.JobHours = nonNil(p.jobHours)
	h.TopCallers = topUse(p.callers)
	h.TopRepos = topUse(p.repos)
	h.Network = networkUse(h.Provider, p)
	for i := range h.Buckets {
		// The host counts samples with every heavy slot busy; scale that share
		// of the bucket's samples by the span the bucket covers so far.
		b := &h.Buckets[i]
		if b.Samples > 0 {
			b.SaturatedSecond = math.Round(float64(b.saturatedSamples) / float64(b.Samples) * coveredSeconds(b.At, bucket, now))
		}
	}
	h.Usage = summarize(*h)
}

// coveredSeconds clamps the newest, still-filling bucket to now.
func coveredSeconds(at time.Time, bucket time.Duration, now time.Time) float64 {
	end := at.Add(bucket)
	if end.After(now) {
		end = now
	}
	if !end.After(at) {
		return 0
	}
	return end.Sub(at).Seconds()
}

func summarize(h HostCapacity) UsageSummary {
	u := UsageSummary{}
	var cpu, io, heavy float64
	for _, b := range h.Buckets {
		w := float64(b.Samples)
		u.Samples += b.Samples
		cpu += b.CPUAvgPct * w
		io += b.IOWaitAvgPct * w
		heavy += b.HeavyAvg * w
		if u.CPUPeakPct == nil || b.CPUPeakPct > *u.CPUPeakPct {
			v := b.CPUPeakPct
			u.CPUPeakPct = &v
		}
		if u.MemAvailMinBytes == nil || b.MemAvailMin < *u.MemAvailMinBytes {
			v := b.MemAvailMin
			u.MemAvailMinBytes = &v
		}
		if u.HeavyPeak == nil || b.HeavyMax > *u.HeavyPeak {
			v := b.HeavyMax
			u.HeavyPeak = &v
		}
		u.SaturatedSeconds += b.SaturatedSecond
	}
	if u.Samples > 0 {
		n := float64(u.Samples)
		c, i := round2(cpu/n), round2(io/n)
		u.CPUAvgPct, u.IOWaitAvgPct = &c, &i
		if h.HeavySlots > 0 {
			o := round2(heavy / n / float64(h.HeavySlots) * 100)
			u.HeavyOccupancyPct = &o
		}
	}
	u.SaturatedSeconds = math.Round(u.SaturatedSeconds)
	for _, j := range h.JobHours {
		u.Jobs += j.Jobs
		u.FailedJobs += j.Failed
		u.SetupFailures += j.Setup
		u.BusySeconds += j.BusySeconds
	}
	return u
}

func networkUse(provider string, p capacityProbeData) *NetworkUse {
	iface := p.defaultIface
	if _, ok := p.ifaces[iface]; !ok {
		iface = ""
		var best uint64
		for name, c := range p.ifaces {
			if name == "lo" || strings.HasPrefix(name, "tailscale") || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "br-") {
				continue
			}
			if c.rx+c.tx >= best {
				best, iface = c.rx+c.tx, name
			}
		}
	}
	c, ok := p.ifaces[iface]
	if !ok {
		return nil
	}
	n := &NetworkUse{Interface: iface, RxBytesSinceBoot: c.rx, TxBytesSinceBoot: c.tx, Series: []NetPoint{}}
	if v, ok := p.links[iface]; ok {
		n.LinkMbps = &v
	}
	if t, ok := p.ifaces["tailscale0"]; ok {
		rx, tx := t.rx, t.tx
		n.TailnetRxBytes, n.TailnetTxBytes = &rx, &tx
	}
	if p.uptime > 0 {
		days := p.uptime / 86400
		n.RxBytesPerDay = math.Round(float64(c.rx) / days)
		n.TxBytesPerDay = math.Round(float64(c.tx) / days)
	}
	n.Allowance = trafficAllowance(provider, n.LinkMbps)
	perDay := n.RxBytesPerDay + n.TxBytesPerDay
	if n.Allowance.Basis == "outbound" {
		perDay = n.TxBytesPerDay
	}
	n.ProjectedMonthBytes = math.Round(perDay * daysPerMonth)
	if n.Allowance.IncludedPerMonth != nil && *n.Allowance.IncludedPerMonth > 0 {
		pct := round1(n.ProjectedMonthBytes / float64(*n.Allowance.IncludedPerMonth) * 100)
		n.ProjectedAllowancePct = &pct
	}
	n.bootID = p.bootID
	return n
}

const terabyte = uint64(1_000_000_000_000)

// Published terms, checked 2026-11-15 against Hetzner's Robot traffic docs:
// root servers with a 1 Gbit/s uplink have unlimited traffic; a 10 Gbit/s
// uplink includes 20 TB/month. Cloud servers in EU locations include 20 TB
// of outbound traffic per month (inbound is free).
func trafficAllowance(provider string, linkMbps *int) TrafficAllowance {
	switch provider {
	case "hetzner-dedicated":
		if linkMbps != nil && *linkMbps >= 10000 {
			v := 20 * terabyte
			return TrafficAllowance{Kind: "monthly_included", Basis: "total", IncludedPerMonth: &v, Note: "Hetzner root server, 10 Gbit/s uplink: 20 TB/month included, overage billed per TB."}
		}
		return TrafficAllowance{Kind: "unmetered", Note: "Hetzner root server, 1 Gbit/s uplink: unlimited traffic. The port speed is the only limit."}
	case "hetzner-cloud":
		v := 20 * terabyte
		return TrafficAllowance{Kind: "monthly_included", Basis: "outbound", IncludedPerMonth: &v, Note: "Hetzner Cloud (EU location): 20 TB outbound/month included; inbound is free."}
	default:
		return TrafficAllowance{Kind: "unknown", Note: "No published traffic terms recorded for this provider."}
	}
}

func hostAlerts(h HostCapacity, w CapacityWindow) []CapacityAlert {
	var out []CapacityAlert
	add := func(sev, code, title, detail string, since, until *time.Time) {
		out = append(out, CapacityAlert{Severity: sev, Code: code, Host: h.Host, Title: title, Detail: detail, Since: since, Until: until})
	}
	active := h.State == "active"
	if !h.Reachable {
		sev := "info"
		if active {
			sev = "critical"
		}
		add(sev, "host_unreachable", "Runner did not answer", "The read-only capacity probe failed over SSH ("+h.Error+").", nil, nil)
		return out
	}
	if h.Load1 > loadHighPerCPU*float64(h.CPUs) {
		add("warning", "load_high", "Load above capacity now", fmt.Sprintf("1-minute load %.1f on %d CPUs.", h.Load1, h.CPUs), nil, nil)
	}
	if h.MemTotalBytes > 0 && float64(h.MemAvailBytes) < memoryPressureRatio*float64(h.MemTotalBytes) {
		add("critical", "memory_low_now", "Memory nearly exhausted now", fmt.Sprintf("%s available of %s.", gib(h.MemAvailBytes), gib(h.MemTotalBytes)), nil, nil)
	}
	if h.Disk != nil {
		switch {
		case h.Disk.UsedPct >= diskCriticalPct:
			add("critical", "disk_high", "Build disk nearly full", fmt.Sprintf("%s is %.0f%% used (%s free).", h.Disk.Mount, h.Disk.UsedPct, gib(h.Disk.AvailBytes)), nil, nil)
		case h.Disk.UsedPct >= diskWarnPct:
			add("warning", "disk_high", "Build disk filling", fmt.Sprintf("%s is %.0f%% used (%s free).", h.Disk.Mount, h.Disk.UsedPct, gib(h.Disk.AvailBytes)), nil, nil)
		}
	}
	// Spike episodes: consecutive flagged buckets merge into one episode.
	episodes := func(code, sev, title string, flagged func(UsageBucket) (bool, float64), describe func(n int, worst float64) string) {
		var count int
		var worst float64
		var since, until *time.Time
		var open bool
		for _, b := range h.Buckets {
			hit, v := flagged(b)
			// A missing bucket (no samples) ends an episode.
			if open && until != nil && b.At.After(*until) {
				open = false
			}
			if hit {
				if !open {
					count++
					s := b.At
					since = &s
					open = true
				}
				e := b.At.Add(w.Bucket)
				until = &e
				worst = math.Max(worst, v)
			} else {
				open = false
			}
		}
		if count > 0 {
			add(sev, code, title, describe(count, worst), since, until)
		}
	}
	episodes("cpu_spike", "warning", "CPU spikes", func(b UsageBucket) (bool, float64) {
		return b.CPUAvgPct >= cpuSpikePct, b.CPUAvgPct
	}, func(n int, worst float64) string {
		return fmt.Sprintf("%d episode(s) with bucket CPU average at or above %.0f%% (worst %.0f%%). Latest shown.", n, cpuSpikePct, worst)
	})
	if h.HeavySlots > 0 {
		episodes("heavy_saturated", "warning", "Heavy slots full", func(b UsageBucket) (bool, float64) {
			return b.HeavyMax >= h.HeavySlots, float64(b.HeavyMax)
		}, func(n int, _ float64) string {
			return fmt.Sprintf("All %d heavy slots were busy in %d episode(s), about %s in total. New heavy work queued during these periods.", h.HeavySlots, n, humanSeconds(h.Usage.SaturatedSeconds))
		})
	}
	episodes("iowait_high", "warning", "Disk I/O wait high", func(b UsageBucket) (bool, float64) {
		return b.IOWaitAvgPct >= iowaitHighPct, b.IOWaitAvgPct
	}, func(n int, worst float64) string {
		return fmt.Sprintf("%d episode(s) with I/O wait at or above %.0f%% (worst %.0f%%).", n, iowaitHighPct, worst)
	})
	if h.MemTotalBytes > 0 {
		limit := memoryPressureRatio * float64(h.MemTotalBytes)
		episodes("memory_pressure", "critical", "Memory pressure", func(b UsageBucket) (bool, float64) {
			return float64(b.MemAvailMin) < limit, float64(h.MemTotalBytes - min(b.MemAvailMin, h.MemTotalBytes))
		}, func(n int, _ float64) string {
			return fmt.Sprintf("Available memory fell below %.0f%% of %s in %d episode(s).", memoryPressureRatio*100, gib(h.MemTotalBytes), n)
		})
	}
	if n := h.Network; n != nil {
		if n.LinkMbps != nil && *n.LinkMbps > 0 {
			link := float64(*n.LinkMbps) * 1e6 / 8
			for _, p := range []struct {
				name string
				v    *float64
			}{{"inbound", n.PeakRxBps}, {"outbound", n.PeakTxBps}} {
				if p.v != nil && *p.v >= linkWarnRatio*link {
					add("warning", "link_busy", "Network port near its limit", fmt.Sprintf("Peak %s rate %s/s is %.0f%% of the %d Mbit/s port.", p.name, humanBytes(*p.v), *p.v/link*100, *n.LinkMbps), nil, nil)
				}
			}
		}
		if n.ProjectedAllowancePct != nil {
			pct := *n.ProjectedAllowancePct
			detail := fmt.Sprintf("Projected %s/month (%s basis, from the since-boot average) is %.0f%% of the %s included.", humanBytes(n.ProjectedMonthBytes), n.Allowance.Basis, pct, humanBytes(float64(*n.Allowance.IncludedPerMonth)))
			switch {
			case pct >= allowanceCritRatio*100:
				add("critical", "traffic_allowance", "Traffic allowance at risk", detail, nil, nil)
			case pct >= allowanceWarnRatio*100:
				add("warning", "traffic_allowance", "Traffic allowance filling", detail, nil, nil)
			}
		}
	}
	u := h.Usage
	if u.SetupFailures > 0 {
		add("warning", "setup_failures", "Jobs failed during setup", fmt.Sprintf("%d of %d jobs in the last %s failed before running (exit SETUP).", u.SetupFailures, u.Jobs, w.Name), nil, nil)
	}
	if u.Jobs >= jobFailureMinimumJobs && float64(u.FailedJobs) >= jobFailureWarnRatio*float64(u.Jobs) {
		add("info", "job_failure_rate", "Many jobs exit non-zero", fmt.Sprintf("%d of %d jobs in the last %s exited non-zero. Test failures count here; check whether they are expected.", u.FailedJobs, u.Jobs, w.Name), nil, nil)
	}
	if active && !h.SamplesPresent {
		add("info", "samples_missing", "No utilization samples", "This runner does not record host_samples, so spikes and use over time are not measured.", nil, nil)
	}
	if active && w.Span >= 24*time.Hour && u.CPUAvgPct != nil && *u.CPUAvgPct < underusedCPUPct && (u.HeavyOccupancyPct == nil || *u.HeavyOccupancyPct < underusedHeavyPct) {
		add("info", "underused", "Runner mostly idle", fmt.Sprintf("CPU averaged %.1f%% and heavy slots %.1f%% full over the last %s, with %d jobs. Capacity is available for more remote work.", *u.CPUAvgPct, deref(u.HeavyOccupancyPct), w.Name, u.Jobs), nil, nil)
	}
	return out
}

func sortAlerts(a []CapacityAlert) {
	rank := map[string]int{"critical": 0, "warning": 1, "info": 2}
	sort.SliceStable(a, func(i, j int) bool {
		if rank[a[i].Severity] != rank[a[j].Severity] {
			return rank[a[i].Severity] < rank[a[j].Severity]
		}
		return a[i].Host < a[j].Host
	})
}

func topUse(rows []UseRow) []UseRow {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].BusySeconds != rows[j].BusySeconds {
			return rows[i].BusySeconds > rows[j].BusySeconds
		}
		return rows[i].Name < rows[j].Name
	})
	if len(rows) > maxUseRows {
		rows = rows[:maxUseRows]
	}
	return nonNil(rows)
}

func nonNil[T any](rows []T) []T {
	if rows == nil {
		return []T{}
	}
	return rows
}

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }

func gib(b uint64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }

func humanBytes(b float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for b >= 1000 && i < len(units)-1 {
		b /= 1000
		i++
	}
	return fmt.Sprintf("%.1f %s", b, units[i])
}

func humanSeconds(s float64) string {
	d := time.Duration(s) * time.Second
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(s))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}

// CapacityText is the human rendering of a capacity report.
func CapacityText(r CapacityReport) string {
	var b strings.Builder
	if r.Error != "" {
		fmt.Fprintf(&b, "Runner capacity unavailable: %s\n", r.Error)
		return b.String()
	}
	fmt.Fprintf(&b, "Runner capacity, last %s (inventory %s)\n", r.Window, r.Generation)
	for _, h := range r.Hosts {
		if !h.Reachable {
			fmt.Fprintf(&b, "  %s [%s] unreachable: %s\n", h.Host, h.State, h.Error)
			continue
		}
		u := h.Usage
		cpu := "no samples"
		if u.CPUAvgPct != nil {
			cpu = fmt.Sprintf("cpu avg %.1f%% peak %.0f%%, heavy %.1f%% full, saturated %s", *u.CPUAvgPct, deref(u.CPUPeakPct), deref(u.HeavyOccupancyPct), humanSeconds(u.SaturatedSeconds))
		}
		fmt.Fprintf(&b, "  %s [%s] %d jobs (%d failed, %d setup), %s\n", h.Host, h.State, u.Jobs, u.FailedJobs, u.SetupFailures, cpu)
		if h.Disk != nil {
			fmt.Fprintf(&b, "    disk %s %.0f%% used, %s free; mem %s of %s free; load %.2f\n", h.Disk.Mount, h.Disk.UsedPct, humanBytes(float64(h.Disk.AvailBytes)), gib(h.MemAvailBytes), gib(h.MemTotalBytes), h.Load1)
		}
		if n := h.Network; n != nil {
			fmt.Fprintf(&b, "    net %s: %s/day in, %s/day out since boot; %s\n", n.Interface, humanBytes(n.RxBytesPerDay), humanBytes(n.TxBytesPerDay), n.Allowance.Kind)
		}
	}
	if len(r.Alerts) == 0 {
		b.WriteString("No alerts.\n")
	}
	for _, a := range r.Alerts {
		fmt.Fprintf(&b, "%s %s: %s. %s\n", strings.ToUpper(a.Severity), a.Host, a.Title, a.Detail)
	}
	if r.HistoryError != "" {
		fmt.Fprintf(&b, "Network history: %s\n", r.HistoryError)
	}
	return b.String()
}
