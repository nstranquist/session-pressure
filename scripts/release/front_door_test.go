package main

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/nstranquist/session-pressure/frontdoor"
)

// fakeHost plays a front door: republish moves the revision it links, and
// its doctor reports that revision next to the installed CLI and helper.
type fakeHost struct {
	linked        string
	afterBuild    string // what the republish links without a pin (its HEAD)
	honoursPin    bool   // the republish links frontdoor.PinEnv when set
	installed     string
	republishErr  error
	omitFrontDoor bool
	calls         [][]string
	envs          [][]string
}

func (h *fakeHost) run(argv, env []string, _ io.Writer) ([]byte, error) {
	h.calls = append(h.calls, append([]string(nil), argv...))
	h.envs = append(h.envs, env)
	switch argv[0] {
	case "republish":
		if h.republishErr != nil {
			return []byte("build log\ncompile error: boom"), h.republishErr
		}
		h.linked = h.afterBuild
		for _, item := range env {
			if pin, ok := strings.CutPrefix(item, frontdoor.PinEnv+"="); ok && h.honoursPin {
				h.linked = pin
			}
		}
		return []byte("built"), nil
	case "/opt/wrapper":
		front := h.linked
		if h.omitFrontDoor {
			front = ""
		}
		mismatch := front != "" && front != h.installed
		return []byte(fmt.Sprintf(`{"ok":true,"doctor":{"source_revisions":{"cli":%q,"front_door":%q,"installed_helper":%q,"checked":true,"mismatch":%v}}}`,
			h.installed, front, h.installed, mismatch)), nil
	}
	return nil, fmt.Errorf("unexpected command %v", argv)
}

func wrapperStatus() frontdoor.Status {
	return frontdoor.Status{
		Registration: frontdoor.Registration{
			Name:      "wrapper",
			Invoke:    []string{"/opt/wrapper", "session", "pressure"},
			Republish: []string{"republish", "--committed"},
		},
		BinaryPresent: true,
	}
}

var (
	oldRev    = strings.Repeat("a", 40)
	targetRev = strings.Repeat("b", 40)
)

func TestPromoteFrontDoorRepublishesAndVerifiesThroughIt(t *testing.T) {
	host := &fakeHost{linked: oldRev, afterBuild: targetRev, installed: targetRev}
	env := []string{"PATH=/bin"}
	receipt, err := promoteFrontDoor(host.run, wrapperStatus(), targetRev, targetRev, env)
	if err != nil {
		t.Fatalf("promoteFrontDoor: %v", err)
	}
	if receipt.Status != frontDoorRepublished || receipt.Revision != targetRev || receipt.Name != "wrapper" {
		t.Fatalf("receipt = %+v", receipt)
	}
	want := [][]string{{"republish", "--committed"}, {"/opt/wrapper", "session", "pressure", "--json", "doctor"}}
	if !reflect.DeepEqual(host.calls, want) {
		t.Fatalf("calls = %v, want %v", host.calls, want)
	}
	wantEnvs := [][]string{append(append([]string(nil), env...), frontdoor.PinEnv+"="+targetRev), env}
	if !reflect.DeepEqual(host.envs, wantEnvs) {
		t.Fatalf("child envs = %v, want the stripped env with the pin for the republish only: %v", host.envs, wantEnvs)
	}
}

// A commit that lands in the session-pressure checkout while the front door
// builds moves the HEAD that an unpinned build links. The pin keeps the
// front door on the promoted revision; a republish that ignores it fails.
func TestPromoteFrontDoorPinsThePromotedRevisionWhenHeadMoves(t *testing.T) {
	movedRev := strings.Repeat("c", 40)
	host := &fakeHost{linked: oldRev, afterBuild: movedRev, honoursPin: true, installed: targetRev}
	receipt, err := promoteFrontDoor(host.run, wrapperStatus(), targetRev, targetRev, nil)
	if err != nil {
		t.Fatalf("promoteFrontDoor with a pinned republish: %v", err)
	}
	if receipt.Status != frontDoorRepublished || receipt.Revision != targetRev {
		t.Fatalf("receipt = %+v, want the front door on %s", receipt, targetRev)
	}

	unpinned := &fakeHost{linked: oldRev, afterBuild: movedRev, installed: targetRev}
	receipt, err = promoteFrontDoor(unpinned.run, wrapperStatus(), targetRev, targetRev, nil)
	if err == nil || !strings.Contains(err.Error(), "links "+movedRev[:12]) || !strings.Contains(err.Error(), frontdoor.PinEnv) {
		t.Fatalf("err = %v, want the moved revision and %s named", err, frontdoor.PinEnv)
	}
	if receipt.Status == frontDoorRepublished {
		t.Fatalf("a front door on the moved HEAD was reported as %s", receipt.Status)
	}
}

func TestCheckPromotedSourceLagAllowsOnlyAMovedHead(t *testing.T) {
	movedRev := strings.Repeat("c", 40)
	cases := map[string]struct {
		lag       doctorSourceLag
		head      string
		wantMoved string
		wantErr   bool
	}{
		"current":                   {lag: doctorSourceLag{Checked: true, SourceRevision: targetRev, InstalledRevision: targetRev}, head: targetRev},
		"head moved after install":  {lag: doctorSourceLag{Checked: true, CommitsBehind: 1, SourceRevision: movedRev, InstalledRevision: targetRev}, head: targetRev, wantMoved: movedRev},
		"install stayed behind":     {lag: doctorSourceLag{Checked: true, CommitsBehind: 1, SourceRevision: targetRev, InstalledRevision: oldRev}, head: targetRev, wantErr: true},
		"behind with no revisions":  {lag: doctorSourceLag{Checked: true, CommitsBehind: 2}, head: targetRev, wantErr: true},
		"behind and head moved too": {lag: doctorSourceLag{Checked: true, CommitsBehind: 2, SourceRevision: movedRev, InstalledRevision: oldRev}, head: targetRev, wantErr: true},
		"promoting a non-HEAD ref":  {lag: doctorSourceLag{Checked: true, CommitsBehind: 3, SourceRevision: movedRev, InstalledRevision: targetRev}, head: oldRev},
		"lag not checked":           {lag: doctorSourceLag{CommitsBehind: 1}, head: targetRev},
		"dirty install of target":   {lag: doctorSourceLag{Checked: true, CommitsBehind: 1, SourceRevision: movedRev, InstalledRevision: targetRev + "-dirty"}, head: targetRev, wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			lag := tc.lag
			moved, err := checkPromotedSourceLag(&lag, targetRev, tc.head)
			if (err != nil) != tc.wantErr || moved != tc.wantMoved {
				t.Fatalf("checkPromotedSourceLag = %q, %v; want %q, error %v", moved, err, tc.wantMoved, tc.wantErr)
			}
		})
	}
}

func TestPromoteFrontDoorFailsWhenTheFrontDoorStaysBehind(t *testing.T) {
	cases := map[string]struct {
		host *fakeHost
		want string
	}{
		"republish fails": {
			host: &fakeHost{linked: oldRev, installed: targetRev, republishErr: errors.New("exit status 1")},
			want: "compile error: boom",
		},
		"still old after republish": {
			host: &fakeHost{linked: oldRev, afterBuild: oldRev, installed: targetRev},
			want: "links " + oldRev[:12] + " after republish",
		},
		"no front-door revision": {
			host: &fakeHost{linked: oldRev, afterBuild: targetRev, installed: targetRev, omitFrontDoor: true},
			want: frontdoor.RevisionEnv,
		},
		"cli and helper disagree with it": {
			host: &fakeHost{linked: oldRev, afterBuild: targetRev, installed: oldRev},
			want: "disagree",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			receipt, err := promoteFrontDoor(tc.host.run, wrapperStatus(), targetRev, targetRev, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			if receipt.Status == frontDoorRepublished {
				t.Fatalf("a failed front door was reported as %s", receipt.Status)
			}
		})
	}
}

func TestPromoteFrontDoorReportsWithoutRunningWhenItCannotHelp(t *testing.T) {
	missing := wrapperStatus()
	missing.BinaryPresent = false
	missing.Detail = "the front door executable is gone"
	unreadable := frontdoor.Status{Path: "/state/front-door.json", Detail: "decode: bad"}

	cases := map[string]struct {
		status frontdoor.Status
		head   string
		want   string
	}{
		"binary removed":           {missing, targetRev, frontDoorBinaryMissing},
		"record unreadable":        {unreadable, targetRev, frontDoorUnreadable},
		"promoting a non-HEAD ref": {wrapperStatus(), oldRev, frontDoorRefNotHead},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			host := &fakeHost{linked: oldRev, afterBuild: targetRev, installed: targetRev}
			receipt, err := promoteFrontDoor(host.run, tc.status, targetRev, tc.head, nil)
			if err != nil {
				t.Fatalf("err = %v; a front door that cannot be rebuilt here is reported, not fatal", err)
			}
			if receipt.Status != tc.want || receipt.Detail == "" {
				t.Fatalf("receipt = %+v, want status %s with a detail", receipt, tc.want)
			}
			if len(host.calls) != 0 {
				t.Fatalf("ran %v; want no commands", host.calls)
			}
		})
	}
}

func TestRunDoctorDecodesTheRecordedFrontDoor(t *testing.T) {
	run := func(argv, env []string, _ io.Writer) ([]byte, error) {
		return []byte(`{"ok":true,"doctor":{"source_revisions":{"cli":"x","mismatch":false},` +
			`"front_door":{"name":"wrapper","invoke":["/opt/wrapper","session","pressure"],"republish":["republish"],` +
			`"path":"/state/front-door.json","binary_present":true}}}`), nil
	}
	report, err := runDoctor(run, []string{"cli", "--json", "doctor"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := report.Doctor.FrontDoor
	if got == nil || got.Name != "wrapper" || !got.BinaryPresent || got.Invoke[0] != "/opt/wrapper" || got.Republish[0] != "republish" {
		t.Fatalf("front door = %+v", got)
	}
}
