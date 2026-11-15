package sessionpressure

import (
	"strings"
	"testing"
	"time"
)

func TestClassifyLightWorkArgv(t *testing.T) {
	cases := []struct {
		argv []string
		want WorkClass
		ok   bool
	}{
		{[]string{"npm", "ci", "--no-audit", "--no-fund"}, WorkClassInstall, true},
		{[]string{"pnpm", "install", "--frozen-lockfile"}, WorkClassInstall, true},
		{[]string{"npm", "i"}, WorkClassInstall, true},
		{[]string{"CI=1", "npm", "ci"}, WorkClassInstall, true},
		{[]string{"env", "CI=1", "pnpm", "install"}, WorkClassInstall, true},
		{[]string{"go", "mod", "download"}, WorkClassInstall, true},
		{[]string{"xcrun", "devicectl", "device", "info", "details", "--device", "X"}, WorkClassExpressTest, true},
		{[]string{"xcrun", "devicectl", "list", "devices"}, WorkClassExpressTest, true},
		{[]string{"xcrun", "simctl", "list", "devices", "available"}, WorkClassExpressTest, true},
		{[]string{"/usr/bin/xcrun", "--show-sdk-path"}, WorkClassExpressTest, true},
		{[]string{"xcrun", "--find", "swiftc"}, WorkClassExpressTest, true},
		// Shapes that change state or run project code keep the caller's class.
		{[]string{"npm", "install", "left-pad"}, "", false},
		{[]string{"npm", "run", "build"}, "", false},
		{[]string{"xcrun", "devicectl", "device", "install", "app", "--device", "X", "A.app"}, "", false},
		{[]string{"xcrun", "devicectl", "device", "process", "launch"}, "", false},
		{[]string{"xcrun", "simctl", "boot", "iPhone"}, "", false},
		{[]string{"xcrun", "xcodebuild", "build"}, "", false},
		{[]string{"xcrun", "swiftc", "-O", "main.swift"}, "", false},
		{[]string{"go", "mod", "tidy"}, "", false},
		{[]string{"npm"}, "", false},
	}
	for _, tc := range cases {
		got, ok := classifyLightWorkArgv(tc.argv)
		if got != tc.want || ok != tc.ok {
			t.Errorf("classifyLightWorkArgv(%q) = %q, %v; want %q, %v", tc.argv, got, ok, tc.want, tc.ok)
		}
	}
}

func TestResolveWorkClassDemotesLightCommandsFromCoarseCallerClass(t *testing.T) {
	// The 2026-11-15 live holds: a device query queued as a weight-5 build and
	// a dependency install queued as a test.
	if got := ResolveWorkClass(WorkClassBuild, false, []string{"bash", "-c", "xcrun devicectl device info details --device X"}); got != WorkClassExpressTest {
		t.Fatalf("wrapped device query resolved to %q", got)
	}
	if got := ResolveWorkClass(WorkClassTest, false, []string{"npm", "ci", "--no-audit", "--no-fund"}); got != WorkClassInstall {
		t.Fatalf("npm ci resolved to %q", got)
	}
	// An explicit heavy request is an operator statement and is kept.
	if got := ResolveWorkClass(WorkClassHeavy, false, []string{"npm", "ci"}); got != WorkClassHeavy {
		t.Fatalf("heavy request changed to %q", got)
	}
	// A compound command takes its strongest statement: an install followed by
	// a full test suite is still a test.
	if got := ResolveWorkClass(WorkClassBuild, false, []string{"bash", "-c", "npm ci && npm test"}); got == WorkClassInstall {
		t.Fatalf("compound install+test demoted to install")
	}
}

func TestWorkToolFamily(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"go", "test", "./..."}, ToolFamilyGo},
		{[]string{"npm", "ci"}, ToolFamilyNode},
		{[]string{"npx", "vitest", "run"}, ToolFamilyNode},
		{[]string{"CI=1", "pnpm", "test"}, ToolFamilyNode},
		{[]string{"swift", "build"}, ToolFamilySwift},
		{[]string{"xcrun", "xcodebuild", "-scheme", "A"}, ToolFamilyXcodebuild},
		{[]string{"xcrun", "devicectl", "list", "devices"}, ToolFamilyXcrun},
		{[]string{"cargo", "test"}, ToolFamilyRust},
		{[]string{"make", "test"}, ToolFamilyMake},
		{[]string{"ndev", "--json", "repo", "architecture"}, ToolFamilyNDev},
		{[]string{"/opt/tool/unknown"}, ToolFamilyOther},
		// The hook-wrapped compound from 2026-11-15: the heavy step names it.
		{[]string{"bash", "-c", "python3 edit.py && npx vitest run src"}, ToolFamilyNode},
		{[]string{"bash", "-c", "xcrun devicectl device info details"}, ToolFamilyXcrun},
	}
	for _, tc := range cases {
		got := WorkToolFamily(tc.argv)
		if got != tc.want {
			t.Errorf("WorkToolFamily(%q) = %q, want %q", tc.argv, got, tc.want)
		}
		if !ValidToolFamily(got) {
			t.Errorf("WorkToolFamily(%q) returned %q outside the vocabulary", tc.argv, got)
		}
	}
}

func TestSummarizeToolFamilies(t *testing.T) {
	events := []WorkEvent{
		{OperationID: "a", Event: WorkEventQueued, ToolFamily: ToolFamilyNode},
		{OperationID: "a", Event: WorkEventAcquired, ToolFamily: ToolFamilyNode, WaitMilliseconds: 9000},
		{OperationID: "a", Event: WorkEventCompleted, ToolFamily: ToolFamilyNode, RuntimeMillis: 100},
		{OperationID: "b", Event: WorkEventQueued, ToolFamily: ToolFamilyGo},
		{OperationID: "b", Event: WorkEventCancelled, ToolFamily: ToolFamilyGo},
		{OperationID: "c", Event: WorkEventAcquired, WaitMilliseconds: 10},
	}
	rows := summarizeToolFamilies(events)
	if len(rows) != 3 || rows[0].ToolFamily != ToolFamilyNode || rows[0].WaitTotalMS != 9000 || rows[0].WaitP95MS != 9000 || rows[0].Completed != 1 {
		t.Fatalf("node row first by wait: %+v", rows)
	}
	found := map[string]WorkToolFamilyStats{}
	for _, row := range rows {
		found[row.ToolFamily] = row
	}
	if found[ToolFamilyGo].Cancelled != 1 || found[ToolFamilyGo].Acquired != 0 || found["unrecorded"].Operations != 1 {
		t.Fatalf("go/unrecorded rows: %+v", found)
	}
}

func TestWorkEventRejectsUnknownToolFamily(t *testing.T) {
	event := WorkEvent{SchemaVersion: WorkEventSchemaVersion, Timestamp: time.Now(), EventID: strings.Repeat("a", 32),
		OperationID: strings.Repeat("b", 32), ToolFamily: "rm -rf /"}
	if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "tool_family") {
		t.Fatalf("free-text tool family accepted: %v", err)
	}
}
