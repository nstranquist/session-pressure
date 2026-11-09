package sessionpressurecmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

func TestRemoteEnablePersistsSelectedEndpoint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	rc, _, errOut := captureMainOutput(t, func() int {
		return cmdSessionPressureRemote(&Flags{}, []string{"enable", "--host", "gaterunner-2", "--user", "builder2"})
	})
	if rc != 0 || errOut != "" {
		t.Fatalf("enable: rc=%d stderr=%q", rc, errOut)
	}
	want, err := sessionpressure.RemoteWorkConfig(dir)
	if err != nil || !want.Enabled || want.Host != "gaterunner-2" || want.User != "builder2" {
		t.Fatalf("persisted endpoint: %+v, %v", want, err)
	}
	for _, args := range [][]string{{"enable", "--host", "bad;host"}, {"enable", "--host"}, {"enable", "--unknown", "x"}} {
		rc, _, _ := captureMainOutput(t, func() int { return cmdSessionPressureRemote(&Flags{}, args) })
		if rc == 0 {
			t.Fatalf("invalid endpoint accepted: %v", args)
		}
		got, err := sessionpressure.RemoteWorkConfig(dir)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("invalid request changed endpoint: %+v, %v", got, err)
		}
	}
}

func TestRemoteHelpDoesNotChangeEndpoint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	before, err := sessionpressure.RemoteWorkConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--help"}, {"enable", "--help"}, {"help"}} {
		rc, out, errOut := captureMainOutput(t, func() int { return cmdSessionPressureRemote(&Flags{}, args) })
		if rc != 0 || !strings.Contains(out, "--host HOST") || errOut != "" {
			t.Fatalf("help: rc=%d stdout=%q stderr=%q", rc, out, errOut)
		}
	}
	after, err := sessionpressure.RemoteWorkConfig(dir)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("help changed endpoint: %+v, %v", after, err)
	}
}
