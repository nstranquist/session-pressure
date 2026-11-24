package sessionpressure

import (
	"context"
	"testing"
	"time"
)

func TestDiskWriterFullNameUsesPathOnlyForCutNames(t *testing.T) {
	cases := []struct {
		name, comm, path, want string
	}{
		{"cut name takes the path base", "nicos-system-bac", "/usr/local/bin/nicos-system-backup", "nicos-system-backup"},
		{"app helper with spaces", "Google Chrome He", "/Applications/Google Chrome.app/Contents/Frameworks/x/Helpers/Google Chrome Helper (Renderer).app/Contents/MacOS/Google Chrome Helper (Renderer)", "Google Chrome Helper (Renderer)"},
		{"a 16-byte name that is whole stays", "plugin-container", "/Applications/Firefox.app/Contents/MacOS/plugin-container.app/Contents/MacOS/plugin-container", "plugin-container"},
		{"shorter names never read the path", "sqlite3", "/usr/bin/sqlite3-other", "sqlite3"},
		{"unreadable path keeps comm", "nicos-delivery-p", "", "nicos-delivery-p"},
		{"a renamed process keeps its own name", "worker-pool-main", "/usr/local/bin/python3.13", "worker-pool-main"},
		{"path base must extend comm, not equal a different prefix", "nicos-delivery-p", "/opt/bin/nicos-delivery", "nicos-delivery-p"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := diskWriterFullName(test.comm, test.path); got != test.want {
				t.Fatalf("diskWriterFullName(%q, %q) = %q, want %q", test.comm, test.path, got, test.want)
			}
		})
	}
}

func TestDiskWriterNameIsCutOf(t *testing.T) {
	if !diskWriterNameIsCutOf("nicos-system-bac", "nicos-system-backup") {
		t.Fatal("the 16-byte prefix is the cut form of the full name")
	}
	for _, pair := range [][2]string{
		{"claude", "2.1.220"},                          // agent identity must still win
		{"nicos-system-bac", "nicos-system-bac"},       // same name
		{"nicos-system-ba", "nicos-system-backup"},     // 15 bytes cannot be a cut
		{"nicos-system-bac", "other-system-backup-xx"}, // not a prefix
	} {
		if diskWriterNameIsCutOf(pair[0], pair[1]) {
			t.Fatalf("diskWriterNameIsCutOf(%q, %q) = true, want false", pair[0], pair[1])
		}
	}
}

// The process sampler names a long program by its cut p_comm. Its ownership
// name must not overwrite the full name the disk scan read from the path,
// but an agent identity from the sampler still names the writer.
func TestDiskWriteOwnershipKeepsFullWriterNames(t *testing.T) {
	harness := newPassHarness(t)
	counter := uint64(0)
	harness.observer.processSource = func(context.Context) (diskProcessSnapshot, error) {
		return diskProcessSnapshot{TotalPIDCount: 2, AccessibleCount: 2, Counters: []diskProcessCounter{
			{PID: 7, StartID: 9, Executable: "nicos-system-backup", BytesWritten: counter},
			{PID: 8, StartID: 10, Executable: "2.1.220", BytesWritten: counter},
		}}, nil
	}
	harness.observer.SetProcessOwnership([]Process{
		{PID: 7, CPUStartID: 9, Executable: "nicos-system-bac"},
		{PID: 8, CPUStartID: 10, Executable: "claude", Agent: "claude"},
	})
	var report DiskWriteReport
	for step := 0; step <= 4; step++ {
		report, _, _ = harness.observer.Sample(context.Background(), harness.start.Add(time.Duration(step)*15*time.Second))
		harness.device += harness.deviceStep
		counter += 1_000
	}
	if got := writerBytes(report, "nicos-system-backup"); got == 0 {
		t.Fatalf("full writer name missing: %+v", report.Writers)
	}
	if got := writerBytes(report, "nicos-system-bac"); got != 0 {
		t.Fatalf("cut ownership name replaced the full name: %+v", report.Writers)
	}
	if got := writerBytes(report, "claude"); got == 0 {
		t.Fatalf("agent identity no longer names its writer: %+v", report.Writers)
	}
}
