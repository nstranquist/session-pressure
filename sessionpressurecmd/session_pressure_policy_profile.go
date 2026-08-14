package sessionpressurecmd

import (
	"fmt"
	"strings"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

func cmdSessionPressurePolicyProfile(g *Flags, runtime pressureRuntime, args []string) int {
	if len(args) == 0 {
		return sessionPressureError("policy profile requires show or apply <name>", 2)
	}
	sub := args[0]
	args = args[1:]
	switch sub {
	case "show":
		if len(args) != 0 {
			return sessionPressureError("policy profile show accepts no arguments", 2)
		}
		profiles := sessionpressure.ListPolicyProfiles()
		payload := map[string]any{
			"ok": true, "action": "policy.profile.show",
			"profiles": profiles,
			"defaults": map[string]any{
				"profile":            sessionpressure.PolicyProfileObserve,
				"enforce_admission":  false,
				"auto_shed_critical": false,
				"warning_derating":   "interactive-only",
			},
		}
		text := "policy profiles:\n"
		for _, p := range profiles {
			text += fmt.Sprintf("  %-22s %s\n", p.Name, p.Description)
		}
		return emitPressure(g, payload, text, 0)
	case "apply":
		if len(args) == 0 {
			return sessionPressureError("policy profile apply requires a profile name", 2)
		}
		name := args[0]
		args = args[1:]
		withAutoShed := false
		dryRun := false
		for _, arg := range args {
			switch arg {
			case "--with-auto-shed":
				withAutoShed = true
			case "--dry-run":
				dryRun = true
			default:
				return sessionPressureError("unknown policy profile apply argument "+strconvQuote(arg), 2)
			}
		}
		if withAutoShed && name != sessionpressure.PolicyProfileBalanced && name != sessionpressure.PolicyProfileThroughput && name != sessionpressure.PolicyProfileInteractive && name != sessionpressure.PolicyProfileDailyDriverEnforce {
			return sessionPressureError("--with-auto-shed is only valid for balanced, throughput, or interactive", 2)
		}
		applied, err := sessionpressure.ApplyPolicyProfile(runtime.policy, name, sessionpressure.ApplyProfileOptions{WithAutoShed: withAutoShed})
		if err != nil {
			return sessionPressureError(err.Error(), 2)
		}
		if dryRun {
			payload := map[string]any{
				"ok": true, "action": "policy.profile.apply.dry_run",
				"profile": name, "policy": applied, "path": runtime.path,
			}
			return emitPressure(g, payload, fmt.Sprintf("dry-run profile %s enforce=%v auto_shed=%v\n", name, applied.EnforceAdmission, applied.AutoShedCritical), 0)
		}
		mutation, err := beginPressurePolicyMutation(runtime.dir)
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		defer mutation.Close()
		runtime = mutation.Runtime
		// Re-apply on freshly locked runtime so concurrent edits do not win.
		applied, err = sessionpressure.ApplyPolicyProfile(runtime.policy, name, sessionpressure.ApplyProfileOptions{WithAutoShed: withAutoShed})
		if err != nil {
			return sessionPressureError(err.Error(), 2)
		}
		if err := sessionpressure.SavePolicy(runtime.path, applied); err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		if err := restartPressureMonitor(runtime.dir); err != nil {
			return sessionPressureError("policy profile applied but resident monitor did not confirm reload: "+err.Error(), 1)
		}
		payload := map[string]any{
			"ok": true, "action": "policy.profile.apply",
			"profile": name, "policy": applied, "path": runtime.path,
		}
		return emitPressure(g, payload, fmt.Sprintf("applied policy profile %s (enforce=%v auto_shed=%v)\n", name, applied.EnforceAdmission, applied.AutoShedCritical), 0)
	default:
		return sessionPressureError("unknown policy profile subcommand "+strconvQuote(sub)+"; want show or apply", 2)
	}
}

func strconvQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
