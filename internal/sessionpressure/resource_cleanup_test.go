package sessionpressure

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestCaptureProcessIdentityRejectsInvalidPID(t *testing.T) {
	if _, err := CaptureProcessIdentity(0); err == nil {
		t.Fatal("invalid PID was accepted")
	}
}

func TestReapClaimedProcessTreeRejectsIdentityMismatchBeforeSignal(t *testing.T) {
	identity, err := CaptureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatalf("CaptureProcessIdentity: %v", err)
	}
	result, err := ReapClaimedProcessTree(context.Background(), ClaimedProcessExpectation{
		RootPID: os.Getpid(), ProcessIdentity: identity + "-wrong", MaxCPUPercent: 10,
	})
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.Signal != "" || result.Result != "revalidation_rejected" {
		t.Fatalf("mismatched identity crossed signal boundary: %#v", result)
	}
}

func TestInspectClaimedProcessTreeRejectsIdentityMismatchWithoutSignal(t *testing.T) {
	identity, err := CaptureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatalf("CaptureProcessIdentity: %v", err)
	}
	result, err := InspectClaimedProcessTree(context.Background(), ClaimedProcessExpectation{
		RootPID: os.Getpid(), ProcessIdentity: identity + "-wrong", MaxCPUPercent: 10,
	})
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.Signal != "" || result.Result != "revalidation_rejected" {
		t.Fatalf("inspection crossed signal boundary: %#v", result)
	}
}

func TestSignalClaimedProcessTreeRejectsReusedDescendantBeforeAnySignal(t *testing.T) {
	pids := []int{10, 11}
	identities := map[int]string{10: "root-generation", 11: "child-generation"}
	signaled := []int{}
	err := signalClaimedProcessTree(pids, identities, func(pid int) (string, error) {
		if pid == 11 {
			return "reused-generation", nil
		}
		return identities[pid], nil
	}, func(pid int) error {
		signaled = append(signaled, pid)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "identity changed before signal") || len(signaled) != 0 {
		t.Fatalf("signaled=%v err=%v", signaled, err)
	}
}

func TestSignalClaimedProcessTreeRevalidatesEveryTargetLeafFirst(t *testing.T) {
	pids := []int{10, 11, 12}
	identities := map[int]string{10: "root", 11: "child", 12: "leaf"}
	reads := map[int]int{}
	signaled := []int{}
	err := signalClaimedProcessTree(pids, identities, func(pid int) (string, error) {
		reads[pid]++
		return identities[pid], nil
	}, func(pid int) error {
		signaled = append(signaled, pid)
		return nil
	})
	if err != nil || !slices.Equal(signaled, []int{12, 11, 10}) {
		t.Fatalf("signaled=%v err=%v", signaled, err)
	}
	for _, pid := range pids {
		if reads[pid] != 2 {
			t.Fatalf("pid %d identity reads=%d, want preflight plus signal-boundary recheck", pid, reads[pid])
		}
	}
}
