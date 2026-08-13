package sessionpressure

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLiveTimeResidentSamplePhases(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	if loaded, persisted, err := LoadPolicy(PolicyPath(filepath.Join(os.Getenv("HOME"), ".nicos-dev", "session-pressure")), 16*1024); err == nil && persisted {
		policy = loaded
	}
	coordinator := NewWorkCoordinator(filepath.Join(os.Getenv("HOME"), ".nicos-dev", "session-pressure"), policy.WorkLimits)
	sampler := NewResidentSampler().WithWorkCoordinator(coordinator)

	timePhase := func(name string, fn func()) time.Duration {
		t.Helper()
		started := time.Now()
		fn()
		elapsed := time.Since(started)
		t.Logf("%-22s %7.1fms", name, float64(elapsed.Microseconds())/1000)
		return elapsed
	}

	for round := 1; round <= 2; round++ {
		t.Logf("---- round %d ----", round)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		var (
			total          time.Duration
			physicalMB     float64
			pressureOutput []byte
			swapUsedMB     float64
			hostCPUErr     error
		)
		total += timePhase("physical", func() {
			var err error
			physicalMB, err = sampler.PhysicalMemoryMB(ctx)
			if err != nil {
				t.Fatalf("physical: %v", err)
			}
		})
		total += timePhase("memory_pressure", func() {
			var err error
			pressureOutput, err = sampler.runner.Run(ctx, "/usr/bin/memory_pressure", "-Q")
			if err != nil {
				t.Fatalf("memory_pressure: %v", err)
			}
		})
		freePercent, err := parseFreePercent(string(pressureOutput))
		if err != nil {
			t.Fatal(err)
		}
		total += timePhase("swap", func() {
			var err error
			swapUsedMB, err = sampler.swapUsedMB(ctx)
			if err != nil {
				t.Fatalf("swap: %v", err)
			}
		})
		forcePower := sampler.role != "resident" || float64(freePercent) <= policy.Thresholds.FreeWarningPercent
		total += timePhase(fmt.Sprintf("power_thermal force=%v", forcePower), func() {
			_ = sampler.samplePowerThermal(ctx, policy, forcePower)
		})
		total += timePhase("host_cpu", func() {
			_, _, hostCPUErr = sampler.sampleHostCPU(ctx, true)
		})
		forceInventory := sampler.role != "resident" ||
			float64(freePercent) <= policy.Thresholds.FreeWarningPercent ||
			sampler.inventoryNeedsPressureRefresh(policy)
		var processes []Process
		var inventoryFresh bool
		total += timePhase(fmt.Sprintf("inventory force=%v", forceInventory), func() {
			var invErr error
			processes, _, inventoryFresh, _, invErr = sampler.processInventory(ctx, policy, forceInventory)
			if invErr != nil {
				t.Logf("inventory err: %v", invErr)
			}
		})
		total += timePhase(fmt.Sprintf("identity fresh=%v", inventoryFresh), func() {
			processes = enrichSampleProcesses(ctx, processes, sampler.sessionStateDir, inventoryFresh, sampler.role)
		})
		var trees []AgentTree
		total += timePhase("trees_consumers", func() {
			trees = buildAgentTrees(processes)
			sampler.recordInventoryPressure(trees)
			_ = buildHostConsumers(processes, trees)
			if len(trees) > maxProjectedAgentTrees {
				trees = trees[:maxProjectedAgentTrees]
			}
			enrichSemanticStates(trees, sampler.sessionStateDir)
		})
		total += timePhase("storage", func() {
			_ = sampler.sampleStorage(policy, LevelNormal)
		})
		total += timePhase("coordinated_work", func() {
			_ = sampler.sampleCoordinatedWork(ctx, processes, time.Now(), inventoryFresh, 10)
		})
		cancel()
		t.Logf("TOTAL                    %7.1fms (physicalMB=%.0f swap=%.0f hostCPUErr=%v procs=%d trees=%d)",
			float64(total.Microseconds())/1000, physicalMB, swapUsedMB, hostCPUErr, len(processes), len(trees))
	}
}
