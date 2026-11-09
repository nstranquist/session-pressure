package sessionpressure

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoteEndpointPolicyCompatibilityAndPersistence(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "remote-work.json"), []byte(`{"enabled":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	old, err := RemoteWorkConfig(dir)
	if err != nil || old.Host != "gaterunner-1" || old.User != "builder" || !old.Enabled {
		t.Fatal(old, err)
	}
	if err := ConfigureRemoteWork(dir, true, "gaterunner-2", "builder"); err != nil {
		t.Fatal(err)
	}
	pinned, err := RemoteWorkConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetRemoteWork(dir, false); err != nil {
		t.Fatal(err)
	}
	current, err := RemoteWorkConfig(dir)
	if err != nil || current.Enabled || current.Host != "gaterunner-2" || pinned.Host != "gaterunner-2" || !pinned.Enabled {
		t.Fatal(current, pinned, err)
	}
	for _, host := range []string{"-oProxyCommand=x", "host;cmd", "user@host", "host\n"} {
		if err := ConfigureRemoteWork(dir, true, host, "builder"); err == nil {
			t.Fatal("accepted unsafe host", host)
		}
	}
}
