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
	if err != nil || old.Host != "gaterunner-2" || old.User != "builder" || !old.Enabled {
		t.Fatal(old, err)
	}
	// An explicit reserve endpoint survives toggling; it must not revert to
	// the default active runner.
	if err := ConfigureRemoteWork(dir, true, "gaterunner-1", "builder"); err != nil {
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
	if err != nil || current.Enabled || current.Host != "gaterunner-1" || pinned.Host != "gaterunner-1" || !pinned.Enabled {
		t.Fatal(current, pinned, err)
	}
	for _, host := range []string{"-oProxyCommand=x", "host;cmd", "user@host", "host\n"} {
		if err := ConfigureRemoteWork(dir, true, host, "builder"); err == nil {
			t.Fatal("accepted unsafe host", host)
		}
	}
}

func TestRemoteFleetPolicySurvivesToggleAndExplicitPin(t *testing.T) {
	dir := t.TempDir()
	p := RemoteWorkPolicy{Enabled: true, Fleet: filepath.Join(dir, "fleet.json")}
	if err := SaveRemoteWorkPolicy(dir, p); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		if err := SetRemoteWork(dir, enabled); err != nil {
			t.Fatal(err)
		}
		got, err := RemoteWorkConfig(dir)
		if err != nil || got.Fleet != p.Fleet || got.Enabled != enabled {
			t.Fatal(got, err)
		}
	}
	if err := ConfigureRemoteWork(dir, true, "gaterunner-3", "builder"); err != nil {
		t.Fatal(err)
	}
	got, err := RemoteWorkConfig(dir)
	if err != nil || got.Fleet != "" || got.Host != "gaterunner-3" {
		t.Fatal(got, err)
	}
	if err := SaveRemoteWorkPolicy(dir, RemoteWorkPolicy{Fleet: "relative.json"}); err == nil {
		t.Fatal("relative inventory accepted")
	}
}
