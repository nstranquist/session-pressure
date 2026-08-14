package sessionpressurecmd

import (
	"context"
	"fmt"
	"time"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

func cmdSessionPressureDoctor(g *Flags, args []string) int {
	if len(args) != 0 {
		return sessionPressureError("doctor accepts no arguments (use --json for machine output)", 2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	runtime, err := loadPressureRuntime(ctx)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	repoRoot, _ := nicosToolsRepoRoot()
	doc, err := sessionpressure.LoadPressureDoctorFromDir(ctx, runtime.dir, repoRoot)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	payload := map[string]any{
		"ok":                   doc.OK,
		"action":               "doctor",
		"schema_version":       doc.SchemaVersion,
		"protection_mode":      doc.ProtectionMode,
		"policy_persisted":     doc.PolicyPersisted,
		"enforce_admission":    doc.EnforceAdmission,
		"auto_shed_critical":   doc.AutoShedCritical,
		"monitor":              doc.Monitor,
		"host":                 doc.Host,
		"work":                 doc.Work,
		"launch_soft_pressure": doc.LaunchSoftPressure,
		"coverage_status":      doc.CoverageStatus,
		"fixes":                doc.Fixes,
		"warnings":             doc.Warnings,
		"health":               doc.Health,
		"doctor":               doc,
	}
	text := fmt.Sprintf(
		"doctor ok=%v protection=%s monitor_healthy=%v host=%s queue=%d/%d depth=%d express_green=%v soft_block=%v noise_suppressed=%v enforce=%v auto_shed=%v\n",
		doc.OK, doc.ProtectionMode, doc.Monitor.Healthy, doc.Host.Level,
		doc.Work.Used, doc.Work.Capacity, doc.Work.QueueDepth, doc.Work.ExpressGreen,
		doc.LaunchSoftPressure.WouldBlock, doc.LaunchSoftPressure.NoiseSuppressed,
		doc.EnforceAdmission, doc.AutoShedCritical,
	)
	for _, fix := range doc.Fixes {
		text += "  fix: " + fix + "\n"
	}
	// Prefer agent-parseable exit 0; hard failures above already return non-zero.
	return emitPressure(g, payload, text, 0)
}
