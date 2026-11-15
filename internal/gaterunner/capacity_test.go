package gaterunner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/runnerfleet"
)

// Shaped from a live runner-3 probe on 2026-11-15 (public addresses
// replaced with documentation ranges). Sample rows carry the 11th column,
// the count of samples with every heavy slot busy.
const capacityProbeFixture = `__META__
12
538653.02 6094005.67
0.18 0.30 0.50 2/396 576512
21d060b6-3a03-409b-8add-f6173aa2d588
__MEM__
MemTotal:       65769568 kB
MemAvailable:   63527664 kB
__DISK__
Filesystem         1-blocks         Used    Available Capacity Mounted on
/dev/md2       996151595008 167837888512 777636552704      18% /
__ROUTE__
default via 192.0.2.1 dev enp9s0 proto static onlink
__NET__
    lo: 2185588989 5078034    0    0    0     0          0         0 2185588989 5078034    0    0    0     0       0          0
enp9s0: 233921541219 182504101    0    3    0     0          0         1 55505768139 96003896    0    0    0     0       0          0
tailscale0: 195302146000 112667749    0    0    0     0          0         0 45071492132 62463085    0    0    0     0       0          0
docker0: 20182654  321662    0    0    0     0          0         0 3812537076  372695    0    0    0     0       0          0
__LINK__
docker0 -1
enp9s0 1000
lo
tailscale0 -1
__SAMPLES__
PRESENT
1794744000 60 43430.9666666667 241755 4899.8 63460122624 0.233333333333333 1 0 12 0
1794744900 60 913218.8 986759 3173.71 62840537088 4.5 5 0 12 30
1794745800 60 931004.6 978905 10080.6 62856081408 4.9 5 1 12 54
1794748500 60 11004.6 78905 10080.6 62856081408 0.1 1 0 12 0
__JOBS__
H 2026-11-15T12 4 1 1 497
C ncd-worker:mac:mac-16g 1 248
C commit-delivery-derived-output 3 249
R nicos-commit-delivery 4 497
__END__
`

func TestParseCapacityProbe(t *testing.T) {
	p, err := parseCapacityProbe(capacityProbeFixture, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if p.cpus != 12 || p.load[0] != 0.18 || p.bootID == "" || p.memTotal != 65769568*1024 || p.memAvail != 63527664*1024 {
		t.Fatalf("meta/mem: %+v", p)
	}
	if p.disk == nil || p.disk.Mount != "/" || p.disk.UsedPct != 17.8 {
		t.Fatalf("disk: %+v", p.disk)
	}
	if p.defaultIface != "enp9s0" || p.links["enp9s0"] != 1000 || p.ifaces["enp9s0"].rx != 233921541219 || p.ifaces["enp9s0"].tx != 55505768139 {
		t.Fatalf("network: iface=%s links=%v ifaces=%v", p.defaultIface, p.links, p.ifaces)
	}
	if _, ok := p.links["docker0"]; ok {
		t.Fatal("negative link speed must be dropped")
	}
	if !p.samplesPresent || len(p.buckets) != 4 || p.buckets[1].CPUAvgPct != 91.3 || p.buckets[1].HeavyMax != 5 || p.buckets[1].saturatedSamples != 30 {
		t.Fatalf("buckets: %+v", p.buckets)
	}
	if len(p.jobHours) != 1 || p.jobHours[0].Jobs != 4 || p.jobHours[0].Setup != 1 || len(p.callers) != 2 || len(p.repos) != 1 {
		t.Fatalf("jobs: %+v %+v %+v", p.jobHours, p.callers, p.repos)
	}
}

func TestParseCapacityProbeRejectsTruncatedAndInvalid(t *testing.T) {
	if _, err := parseCapacityProbe(strings.TrimSuffix(capacityProbeFixture, "__END__\n"), time.Hour); err == nil {
		t.Fatal("truncated probe accepted")
	}
	bad := strings.Replace(capacityProbeFixture, "MemAvailable:   63527664 kB", "MemAvailable:   93527664 kB", 1)
	if _, err := parseCapacityProbe(bad, time.Hour); err == nil {
		t.Fatal("available memory above total accepted")
	}
	absent := strings.Replace(capacityProbeFixture, "PRESENT\n", "ABSENT\n", 1)
	p, err := parseCapacityProbe(absent, time.Hour)
	if err != nil || p.samplesPresent || len(p.buckets) != 0 {
		t.Fatalf("absent samples: err=%v present=%v buckets=%d", err, p.samplesPresent, len(p.buckets))
	}
}

func dedicatedHost(state string) HostCapacity {
	return HostCapacity{Host: "runner-3", State: state, Provider: "hetzner-dedicated", HeavySlots: 5, LightSlots: 2}
}

func alertCodes(a []CapacityAlert) map[string]CapacityAlert {
	out := map[string]CapacityAlert{}
	for _, x := range a {
		out[x.Code] = x
	}
	return out
}

func TestHostAlertsSpikesSaturationAndEpisodes(t *testing.T) {
	p, err := parseCapacityProbe(capacityProbeFixture, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	h := dedicatedHost("active")
	now := time.Unix(1794749000, 0).UTC()
	applyProbe(&h, p, 15*time.Minute, now)
	// Buckets 2 and 3 are adjacent spikes: one episode. Bucket 4 follows a gap.
	if got := h.Buckets[1].SaturatedSecond; got != 450 {
		t.Fatalf("saturated seconds bucket 2 = %v, want 450 (30 of 60 samples over 900 s)", got)
	}
	if h.Usage.SaturatedSeconds != 450+810 || h.Usage.Jobs != 4 || h.Usage.SetupFailures != 1 {
		t.Fatalf("usage: %+v", h.Usage)
	}
	codes := alertCodes(hostAlerts(h, capacityWindows[1]))
	spike, ok := codes["cpu_spike"]
	if !ok || !strings.HasPrefix(spike.Detail, "1 episode(s)") || spike.Since.Unix() != 1794744900 || spike.Until.Unix() != 1794745800+900 {
		t.Fatalf("cpu spike: %+v", spike)
	}
	if _, ok := codes["heavy_saturated"]; !ok {
		t.Fatal("heavy saturation not raised")
	}
	if _, ok := codes["setup_failures"]; !ok {
		t.Fatal("setup failures not raised")
	}
	if _, ok := codes["underused"]; ok {
		t.Fatal("a spiking runner reported as idle")
	}
	if n := h.Network; n == nil || n.Allowance.Kind != "unmetered" || n.ProjectedAllowancePct != nil || n.TailnetRxBytes == nil {
		t.Fatalf("network: %+v", n)
	}
}

func TestHostAlertsIdleUnreachableAndAllowance(t *testing.T) {
	idle := dedicatedHost("active")
	idle.Reachable, idle.CPUs, idle.MemTotalBytes, idle.MemAvailBytes, idle.SamplesPresent = true, 12, 64<<30, 60<<30, true
	idle.Buckets = []UsageBucket{{At: time.Unix(0, 0), Samples: 60, CPUAvgPct: 1, HeavyAvg: 0.1, HeavyMax: 1, MemAvailMin: 60 << 30}}
	idle.Usage = summarize(idle)
	if _, ok := alertCodes(hostAlerts(idle, capacityWindows[1]))["underused"]; !ok {
		t.Fatal("idle active runner not reported")
	}
	if _, ok := alertCodes(hostAlerts(idle, capacityWindows[0]))["underused"]; ok {
		t.Fatal("underuse judged on a window shorter than a day")
	}
	reserve := dedicatedHost("reserve")
	reserve.Error = "runner_capacity_read_failed"
	if a := hostAlerts(reserve, capacityWindows[1]); len(a) != 1 || a[0].Severity != "info" {
		t.Fatalf("reserve unreachable: %+v", a)
	}
	active := dedicatedHost("active")
	active.Error = "runner_capacity_read_failed"
	if a := hostAlerts(active, capacityWindows[1]); len(a) != 1 || a[0].Severity != "critical" {
		t.Fatalf("active unreachable: %+v", a)
	}
	// A cloud runner pushing 600 GB/day outbound projects past 90% of 20 TB.
	p := capacityProbeData{uptime: 86400, ifaces: map[string]ifaceCounters{"eth0": {rx: 1e12, tx: 600e9}}, links: map[string]int{}, defaultIface: "eth0"}
	cloud := HostCapacity{Host: "runner-1", State: "reserve", Provider: "hetzner-cloud", Reachable: true, CPUs: 4, MemTotalBytes: 8 << 30, MemAvailBytes: 7 << 30}
	cloud.Network = networkUse(cloud.Provider, p)
	if cloud.Network.Allowance.Basis != "outbound" || cloud.Network.ProjectedAllowancePct == nil || *cloud.Network.ProjectedAllowancePct < 90 {
		t.Fatalf("cloud allowance: %+v", cloud.Network)
	}
	if a, ok := alertCodes(hostAlerts(cloud, capacityWindows[1]))["traffic_allowance"]; !ok || a.Severity != "critical" {
		t.Fatalf("allowance alert: %+v", a)
	}
}

func TestCapacityStoreSeriesHandlesRebootsGapsAndThrottle(t *testing.T) {
	s, err := openCapacityStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := time.Unix(1794700000, 0).UTC()
	rec := func(off time.Duration, boot string, rx, tx uint64) {
		t.Helper()
		if err := s.record("h", base.Add(off), &NetworkUse{Interface: "eth0", RxBytesSinceBoot: rx, TxBytesSinceBoot: tx, bootID: boot}); err != nil {
			t.Fatal(err)
		}
	}
	rec(0, "a", 1000, 0)
	rec(10*time.Second, "a", 999999, 999999) // throttled: under 30 s after the last sample
	rec(60*time.Second, "a", 61000, 6000)    // 1000 B/s in, 100 B/s out
	rec(120*time.Second, "b", 10, 10)        // reboot: no rate
	rec(180*time.Second, "b", 6010, 610)     // 100 B/s, 10 B/s
	rec(time.Hour, "b", 9e9, 9e9)            // after a 57-minute gap: skipped
	pts, coverage, err := s.series("h", "eth0", base.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 || pts[0].RxBps != 1000 || pts[0].TxBps != 100 || pts[1].RxBps != 100 || coverage != 120 {
		t.Fatalf("series=%+v coverage=%d", pts, coverage)
	}
	if err := s.prune(base.Add(capacityStoreRetention + 2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if pts, _, _ := s.series("h", "eth0", base.Add(-time.Hour)); len(pts) != 0 {
		t.Fatalf("prune kept %d points", len(pts))
	}
}

func TestDownsampleKeepsPeaks(t *testing.T) {
	var pts []NetPoint
	for i := 0; i < 10; i++ {
		pts = append(pts, NetPoint{At: time.Unix(int64(i), 0), RxBps: float64(i % 3)})
	}
	pts[4].RxBps = 99
	got := downsample(pts, 3)
	if len(got) > 3 || got[1].RxBps != 99 {
		t.Fatalf("downsample=%+v", got)
	}
}

func TestCapacityWithFleetAndProbe(t *testing.T) {
	home, data := t.TempDir(), t.TempDir()
	cfg := runnerfleet.Config{Schema: runnerfleet.Schema, Generation: "test-v1", Hosts: []runnerfleet.Host{
		{Name: "runner-3", User: "builder", State: "active", Provider: "hetzner-dedicated", Platform: "linux/amd64", SourceCommit: "0123456789012345678901234567890123456789", Weight: 7, HeavySlots: 5, LightSlots: 2, MaxShards: 4, Repositories: []string{"session-pressure-work"}, Profiles: []string{"linux-native"}},
		{Name: "runner-1", User: "builder", State: "reserve", Provider: "hetzner-cloud", Platform: "linux/arm64", SourceCommit: "0123456789012345678901234567890123456789", HeavySlots: 1, LightSlots: 1, MaxShards: 1, Repositories: []string{"session-pressure-work"}, Profiles: []string{"linux-native"}},
	}}
	path := filepath.Join(home, "fleet.json")
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var scripts []string
	probe := func(_ context.Context, h runnerfleet.Host, script string) ([]byte, error) {
		if h.Name == "runner-1" {
			return nil, errors.New("down")
		}
		scripts = append(scripts, script)
		return []byte(capacityProbeFixture), nil
	}
	ctx := WithFleet(context.Background(), path)
	w, _ := ParseCapacityWindow("24h")
	now := time.Unix(1794749000, 0).UTC()
	first := capacityWith(ctx, home, data, w, now, probe)
	if first.Error != "" || first.HistoryError != "" || len(first.Hosts) != 2 || !first.Hosts[0].Reachable || first.Hosts[1].Reachable {
		t.Fatalf("report=%+v", first)
	}
	if !strings.Contains(scripts[0], "sum(heavy>=5)") || !strings.Contains(scripts[0], "at>=1794662600") || strings.Contains(scripts[0], "%!") {
		t.Fatalf("script not bound to window and slots:\n%s", scripts[0])
	}
	if first.Alerts[0].Severity == "info" {
		t.Fatalf("alerts not sorted by severity: %+v", first.Alerts)
	}
	// A second read a minute later with grown counters yields a rate.
	grown := strings.Replace(capacityProbeFixture, "enp9s0: 233921541219", "enp9s0: 233981541219", 1)
	second := capacityWith(ctx, home, data, w, now.Add(time.Minute), func(c context.Context, h runnerfleet.Host, s string) ([]byte, error) {
		if h.Name == "runner-1" {
			return nil, errors.New("down")
		}
		return []byte(grown), nil
	})
	n := second.Hosts[0].Network
	if n == nil || len(n.Series) != 1 || n.Series[0].RxBps != 1e6 || n.PeakRxBps == nil || n.SeriesCoverageSeconds != 60 {
		t.Fatalf("network series: %+v", n)
	}
	if text := CapacityText(second); !strings.Contains(text, "runner-1 [reserve] unreachable") || !strings.Contains(text, "unmetered") {
		t.Fatalf("text:\n%s", text)
	}
	missing := capacityWith(WithFleet(context.Background(), filepath.Join(home, "none.json")), home, data, w, now, probe)
	if missing.Error != "runner_fleet_inventory_missing" {
		t.Fatalf("missing inventory: %+v", missing.Error)
	}
	if _, err := ParseCapacityWindow("3d"); err == nil {
		t.Fatal("unsupported window accepted")
	}
}

func TestClassifySSHFailures(t *testing.T) {
	run := func(ctx context.Context, script string) error {
		return exec.CommandContext(ctx, "/bin/sh", "-c", script).Run()
	}
	bg := context.Background()
	if got := sshFailureKind(classifySSH(bg, run(bg, "exit 255"))); got != "connect_failed" {
		t.Fatalf("exit 255 = %s", got)
	}
	if got := sshFailureKind(classifySSH(bg, run(bg, "exit 3"))); got != "remote_failed" {
		t.Fatalf("exit 3 = %s", got)
	}
	ctx, cancel := context.WithTimeout(bg, 20*time.Millisecond)
	defer cancel()
	err := run(ctx, "sleep 2")
	if got := sshFailureKind(classifySSH(ctx, err)); got != "timeout" {
		t.Fatalf("deadline = %s", got)
	}
	if got := sshFailureKind(classifySSH(bg, errors.New("gate-runner output exceeds bound"))); got != "output_too_large" {
		t.Fatalf("bound = %s", got)
	}
	if got := sshFailureKind(errors.New("plain")); got != "connect_failed" {
		t.Fatalf("unclassified = %s", got)
	}
}
