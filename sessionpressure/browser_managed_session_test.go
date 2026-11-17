package sessionpressure

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func writeManagedSession(t *testing.T, root, name string, record map[string]any) string {
	t.Helper()
	for _, dir := range []string{filepath.Join(root, "sessions"), filepath.Join(root, "profiles", name)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sessions", name+".json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "profiles", name)
}

func TestManagedSessionLeaseActiveExemptsOnlyLiveIdleLeases(t *testing.T) {
	now := time.Now().UTC()
	started := now.Add(-9 * time.Minute)
	root := t.TempDir()
	record := func(overrides map[string]any) map[string]any {
		base := map[string]any{
			"pid": 4242, "process_started_at": started, "started_at": started.Add(500 * time.Millisecond),
			"last_activity_at": now.Add(-time.Minute), "idle_timeout": "10m0s", "lifecycle_policy": "idle",
		}
		for key, value := range overrides {
			base[key] = value
		}
		return base
	}
	process := func(dir string) browserProcess {
		return browserProcess{PID: 4242, StartedAt: started, UserDataDir: dir}
	}
	cases := []struct {
		name      string
		overrides map[string]any
		want      bool
	}{
		// A CSP sweep's session, 9 minutes old, used a minute ago.
		{"live lease", nil, true},
		// Lease ran out and the watchdog's poll grace has passed.
		{"expired", map[string]any{"started_at": now.Add(-13 * time.Minute), "last_activity_at": now.Add(-12 * time.Minute)}, false},
		{"keep-alive", map[string]any{"lifecycle_policy": "keep_alive"}, false},
		{"other pid", map[string]any{"pid": 9}, false},
		{"pid reused", map[string]any{"process_started_at": started.Add(-time.Hour)}, false},
		{"bad timeout", map[string]any{"idle_timeout": "soon"}, false},
	}
	for i, tc := range cases {
		dir := writeManagedSession(t, root, "s"+strconv.Itoa(i), record(tc.overrides))
		if got := managedSessionLeaseActive(process(dir), now); got != tc.want {
			t.Errorf("%s: active = %v, want %v", tc.name, got, tc.want)
		}
	}
	if managedSessionLeaseActive(browserProcess{PID: 4242, StartedAt: started}, now) {
		t.Error("browser without --user-data-dir exempted")
	}
	if managedSessionLeaseActive(process(filepath.Join(root, "elsewhere", "x")), now) {
		t.Error("profile outside a snapref profiles dir exempted")
	}
	if managedSessionLeaseActive(process(filepath.Join(root, "profiles", "missing")), now) {
		t.Error("profile without a session record exempted")
	}
}

func TestBrowserKernelArgsReturnsUserDataDir(t *testing.T) {
	args := []string{
		"/Applications/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
		"--headless=new", "--remote-debugging-port=0", "--user-data-dir=/Users/example/.nicos-dev/browser/profiles/csp-sweep",
	}
	var body bytes.Buffer
	_ = binary.Write(&body, binary.LittleEndian, uint32(len(args)))
	body.WriteString(args[0])
	body.WriteByte(0)
	for _, arg := range args {
		body.WriteString(arg)
		body.WriteByte(0)
	}
	automated, dir, err := browserKernelArgs(body.Bytes())
	if err != nil || !automated || dir != "/Users/example/.nicos-dev/browser/profiles/csp-sweep" {
		t.Fatalf("browserKernelArgs = %v, %q, %v", automated, dir, err)
	}
}
