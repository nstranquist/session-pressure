package sessionpressurecmd

import (
	"github.com/nstranquist/session-pressure/sessionpressure"
	"testing"
)

func TestCmdSessionPressurePolicyMigrateStorage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	p := sessionpressure.DefaultPolicy(16 * 1024)
	p.EnforceAdmission = true
	p.AutoShedCritical = false
	p.Storage.EnforceAdmission = true
	p.Storage.WarningFreeBytes, p.Storage.RedFreeBytes, p.Storage.CriticalFreeBytes = 20<<30, 10<<30, 5<<30
	p.Storage.WarningReleaseBytes, p.Storage.RedReleaseBytes, p.Storage.CriticalReleaseBytes = 25<<30, 15<<30, 8<<30
	p.Storage.TargetFreeBytes = 25 << 30
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), p); err != nil {
		t.Fatal(err)
	}
	fake := &fakePressureLaunchdController{status: sessionpressure.LaunchdStatus{OK: true, Installed: true, Loaded: true, PID: 77}}
	installFakePressureLaunchd(t, fake)
	rc, _, stderr := captureMainOutput(t, func() int { return cmdSessionPressure(&Flags{JSON: true}, []string{"policy", "migrate"}) })
	if rc != 0 {
		t.Fatalf("migrate rc=%d stderr=%s", rc, stderr)
	}
	got, _, err := sessionpressure.LoadPolicy(sessionpressure.PolicyPath(dir), 16*1024)
	if err != nil || got.Storage.WarningFreeBytes != 50<<30 || got.Storage.TargetFreeBytes != 60<<30 || !got.EnforceAdmission || !got.Storage.EnforceAdmission || got.AutoShedCritical {
		t.Fatalf("policy=%+v err=%v", got, err)
	}
}
