package sessionpressurecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

func TestParseStorageRepositoryPolicyArgs(t *testing.T) {
	got, err := parseStorageRepositoryPolicyArgs([]string{"set", "/repo", "--critical-free", "5GiB", "--reason", " garrid build "})
	if err != nil || !got.set || got.path != "/repo" || got.criticalFree != 5<<30 || got.reason != "garrid build" {
		t.Fatalf("parse = %+v, %v", got, err)
	}
	for _, args := range [][]string{
		nil,
		{"set"},
		{"set", "/repo"},
		{"set", "/repo", "--critical-free"},
		{"set", "/repo", "--critical-free", "5GiB", "--critical-free", "6GiB"},
		{"set", "/repo", "--critical-free", "lots"},
		{"set", "/repo", "--reason", "x"},
		{"set", "--critical-free", "5GiB"},
		{"clear", "/repo", "--critical-free", "5GiB"},
		{"list"},
	} {
		if _, err := parseStorageRepositoryPolicyArgs(args); err == nil {
			t.Fatalf("parse accepted %q", args)
		}
	}
}

func TestCmdSessionPressureStorageRepositoryPolicyLifecycle(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	policy.Storage.EnforceAdmission = true
	policy.Storage.BlockNewAt = sessionpressure.LevelCritical
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	fake := &fakePressureLaunchdController{status: sessionpressure.LaunchdStatus{OK: true, Installed: true, Loaded: true, PID: 77}}
	installFakePressureLaunchd(t, fake)

	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(base, "dev", "garrid")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "apps")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}

	// A subdirectory resolves to the canonical repository key.
	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"storage", "policy", "repository", "set", sub, "--critical-free", "5GiB", "--reason", "garrid build floor"})
	})
	if rc != 0 {
		t.Fatalf("set rc=%d stderr=%s", rc, stderr)
	}
	var setPayload struct {
		Action     string `json:"action"`
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal([]byte(stdout), &setPayload); err != nil || setPayload.Action != "storage.policy.repository.set" || setPayload.Repository != repo {
		t.Fatalf("set payload = %+v err=%v\n%s", setPayload, err, stdout)
	}
	got, _, err := sessionpressure.LoadPolicy(sessionpressure.PolicyPath(dir), 16*1024)
	if err != nil {
		t.Fatal(err)
	}
	override, ok := got.Storage.RepositoryOverrides[repo]
	if !ok || override.CriticalFreeBytes != 5<<30 || override.Reason != "garrid build floor" {
		t.Fatalf("persisted overrides = %+v", got.Storage.RepositoryOverrides)
	}
	if got.Storage.CriticalFreeBytes != policy.Storage.CriticalFreeBytes || got.Storage.BlockNewAt != sessionpressure.LevelCritical || !got.Storage.EnforceAdmission {
		t.Fatalf("machine-wide storage policy changed: %+v", got.Storage)
	}

	// Status JSON exposes the override and its live verdict.
	rc, stdout, stderr = captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"storage", "status"})
	})
	if rc != 0 {
		t.Fatalf("status rc=%d stderr=%s", rc, stderr)
	}
	var status struct {
		Policy struct {
			Overrides map[string]sessionpressure.StorageRepositoryOverride `json:"repository_overrides"`
		} `json:"storage_policy"`
		Repositories []sessionpressure.StorageRepositoryAdmissionStatus `json:"repository_admission"`
	}
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatalf("status json: %v\n%s", err, stdout)
	}
	if status.Policy.Overrides[repo].CriticalFreeBytes != 5<<30 || len(status.Repositories) != 1 || status.Repositories[0].Repository != repo || !status.Repositories[0].Exists {
		t.Fatalf("status = %+v", status)
	}

	// Below the floor minimum is refused without changing the file.
	before, err := os.ReadFile(sessionpressure.PolicyPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	rc, _, stderr = captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"storage", "policy", "repository", "set", repo, "--critical-free", "100MiB"})
	})
	after, _ := os.ReadFile(sessionpressure.PolicyPath(dir))
	if rc == 0 || string(after) != string(before) || !strings.Contains(stderr, "critical_free_bytes") {
		t.Fatalf("tiny floor rc=%d stderr=%s", rc, stderr)
	}
	// A directory outside any repository is refused.
	rc, _, stderr = captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"storage", "policy", "repository", "set", base, "--critical-free", "5GiB"})
	})
	if rc == 0 || !strings.Contains(stderr, "not inside a Git repository") {
		t.Fatalf("non-repository rc=%d stderr=%s", rc, stderr)
	}

	// Clear works by stored key even after the repository is gone.
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	rc, _, stderr = captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"storage", "policy", "repository", "clear", repo})
	})
	if rc != 0 {
		t.Fatalf("clear rc=%d stderr=%s", rc, stderr)
	}
	got, _, err = sessionpressure.LoadPolicy(sessionpressure.PolicyPath(dir), 16*1024)
	if err != nil || len(got.Storage.RepositoryOverrides) != 0 {
		t.Fatalf("after clear overrides=%+v err=%v", got.Storage.RepositoryOverrides, err)
	}
	rc, _, _ = captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"storage", "policy", "repository", "clear", repo})
	})
	if rc == 0 {
		t.Fatal("clearing a missing override succeeded")
	}
}
