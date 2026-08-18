import Foundation

/// Closed-code review counters used to explain an advisory profile suggestion.
/// Extra JSON fields are ignored so compact and full reports both decode.
public struct WorkReviewSignals: Codable, Sendable, Hashable {
    public var cancelledOperations: Int?
    public var wrapperInterruptOperations: Int?
    public var longWaitOperations: Int?
    public var reservationDeferrals: Int?
    public var singleflightWaits: Int?
    public var bypassedAdmissions: Int?

    enum CodingKeys: String, CodingKey {
        case cancelledOperations = "cancelled_operations"
        case wrapperInterruptOperations = "wrapper_interrupt_operations"
        case longWaitOperations = "long_wait_operations"
        case reservationDeferrals = "reservation_deferrals"
        case singleflightWaits = "singleflight_waits"
        case bypassedAdmissions = "bypassed_admissions"
    }

    public init(
        cancelledOperations: Int? = nil,
        wrapperInterruptOperations: Int? = nil,
        longWaitOperations: Int? = nil,
        reservationDeferrals: Int? = nil,
        singleflightWaits: Int? = nil,
        bypassedAdmissions: Int? = nil
    ) {
        self.cancelledOperations = cancelledOperations
        self.wrapperInterruptOperations = wrapperInterruptOperations
        self.longWaitOperations = longWaitOperations
        self.reservationDeferrals = reservationDeferrals
        self.singleflightWaits = singleflightWaits
        self.bypassedAdmissions = bypassedAdmissions
    }
}

public enum PolicySuggestionKind: String, Sendable, Equatable {
    case calibration
    case restoreDefault
}

/// Operator-facing card for a control-plane policy suggestion.
/// Built only from closed codes plus counts — never argv or paths.
public struct PolicySuggestion: Equatable, Sendable {
    public var kind: PolicySuggestionKind
    public var profile: String
    public var reasonCode: String
    public var title: String
    public var headline: String
    public var currentTitle: String
    public var currentFlags: String
    public var explanation: String
    public var tradeoff: String
    public var agentPaste: String
    public var applyCommand: String
    public var dryRunCommand: String
    public var weakensProtection: Bool
    public var alreadyApplied: Bool
    public var withAutoShed: Bool
    public var showsApplyWhenCollapsed: Bool

    public var confirmTitle: String {
        switch kind {
        case .restoreDefault: return "Return to balanced?"
        case .calibration: return "Apply \(title.lowercased())?"
        }
    }

    public var confirmMessage: String {
        [explanation, tradeoff].filter { !$0.isEmpty }.joined(separator: " ")
    }
}

public enum PolicySuggestionFactory {
    public static let multiAgentSoft = "multi-agent-soft"
    public static let balanced = "balanced"

    /// Calibration hint when it is not already live; otherwise a return-to-balanced
    /// card whenever the host is off the daily-driver work style.
    public static func current(from calibration: WorkCalibration?, policy: PressurePolicy?) -> PolicySuggestion? {
        if let suggested = calibrationSuggestion(from: calibration, policy: policy) {
            return suggested
        }
        return restoreDefault(policy: policy)
    }

    static func calibrationSuggestion(from calibration: WorkCalibration?, policy: PressurePolicy?) -> PolicySuggestion? {
        guard let calibration,
              let profile = calibration.suggestedPolicyProfile?.trimmingCharacters(in: .whitespacesAndNewlines),
              !profile.isEmpty
        else { return nil }

        if matchesApplied(profile: profile, policy: policy) { return nil }

        let reason = calibration.suggestedPolicyProfileReason ?? ""
        let title = displayName(profile)
        let liveTitle = currentTitle(policy)
        let liveFlags = currentFlags(policy)
        let explanation = explanationText(profile: profile, reason: reason, calibration: calibration)
        let tradeoff = tradeoffText(profile: profile, policy: policy)
        let apply = "ndev session pressure policy profile apply \(profile)"
        let dryRun = apply + " --dry-run"
        let paste = agentPaste(
            profile: profile,
            reason: reason,
            currentTitle: liveTitle,
            currentFlags: liveFlags,
            explanation: explanation,
            tradeoff: tradeoff,
            calibration: calibration,
            policy: policy,
            apply: apply,
            dryRun: dryRun
        )
        return PolicySuggestion(
            kind: .calibration,
            profile: profile,
            reasonCode: reason,
            title: title,
            headline: "Suggest \(title)",
            currentTitle: liveTitle,
            currentFlags: liveFlags,
            explanation: explanation,
            tradeoff: tradeoff,
            agentPaste: paste,
            applyCommand: apply,
            dryRunCommand: dryRun,
            weakensProtection: weakensProtection(profile: profile, policy: policy),
            alreadyApplied: false,
            withAutoShed: false,
            showsApplyWhenCollapsed: false
        )
    }

    static func restoreDefault(policy: PressurePolicy?) -> PolicySuggestion? {
        guard let policy, policy.enabled, !isDailyDriver(policy) else { return nil }

        let liveTitle = currentTitle(policy)
        let liveFlags = currentFlags(policy)
        let title = displayName(balanced)
        let explanation = "Current work style is \(liveTitle), not the daily-driver default."
        let tradeoff = "Applying balanced turns launch blocking back on. Auto-shed is optional on the confirm step; it restores graceful relief of one old quiet tree at sustained critical."
        let apply = "ndev session pressure policy profile apply \(balanced)"
        let dryRun = apply + " --dry-run"
        let paste = agentPaste(
            profile: balanced,
            reason: "restore_daily_driver",
            currentTitle: liveTitle,
            currentFlags: liveFlags,
            explanation: explanation,
            tradeoff: tradeoff,
            calibration: nil,
            policy: policy,
            apply: apply,
            dryRun: dryRun
        )
        return PolicySuggestion(
            kind: .restoreDefault,
            profile: balanced,
            reasonCode: "restore_daily_driver",
            title: title,
            headline: "Default \(title)",
            currentTitle: liveTitle,
            currentFlags: liveFlags,
            explanation: explanation,
            tradeoff: tradeoff,
            agentPaste: paste,
            applyCommand: apply,
            dryRunCommand: dryRun,
            weakensProtection: false,
            alreadyApplied: false,
            withAutoShed: false,
            showsApplyWhenCollapsed: true
        )
    }

    public static func isDailyDriver(_ policy: PressurePolicy) -> Bool {
        guard policy.enabled, policy.enforceAdmission else { return false }
        let name = policy.profile?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return name.isEmpty || name.caseInsensitiveCompare(balanced) == .orderedSame
    }

    public static func matchesApplied(profile: String, policy: PressurePolicy?) -> Bool {
        guard let policy else { return false }
        if policy.profile?.caseInsensitiveCompare(profile) == .orderedSame {
            return true
        }
        guard profile == multiAgentSoft else { return false }
        return !policy.enforceAdmission
            && !policy.autoShedCritical
            && policy.launchAdmission?.mode == "soft"
            && policy.launchAdmission?.oldestWaitBlockSeconds == 60
    }

    public static func displayName(_ profile: String) -> String {
        profile.replacingOccurrences(of: "-", with: " ").capitalized
    }

    static func weakensProtection(profile: String, policy: PressurePolicy?) -> Bool {
        guard profile == multiAgentSoft else { return false }
        return policy?.enforceAdmission == true || policy?.autoShedCritical == true
    }

    public static func currentTitle(_ policy: PressurePolicy?) -> String {
        guard let policy else { return "Unknown" }
        if let profile = policy.profile?.trimmingCharacters(in: .whitespacesAndNewlines), !profile.isEmpty {
            return displayName(profile)
        }
        return policy.modeLabel
    }

    public static func currentFlags(_ policy: PressurePolicy?) -> String {
        guard let policy else { return "" }
        var flags: [String] = []
        flags.append(policy.enforceAdmission ? "launch blocking on" : "launch blocking off")
        flags.append(policy.autoShedCritical ? "auto-shed on" : "auto-shed off")
        return flags.joined(separator: " · ")
    }

    static func explanationText(
        profile: String,
        reason: String,
        calibration: WorkCalibration
    ) -> String {
        let window = "last 24h"
        let ops = calibration.operationCount ?? 0
        let signals = evidenceBits(calibration)
        let why: String
        switch reason {
        case "high_cancel_rate":
            let cancelled = calibration.reviewSignals?.cancelledOperations ?? 0
            why = "More than 15% of coordinated work was cancelled (\(cancelled) of \(ops) in the \(window))."
        case "multi_agent_queue_pressure":
            why = "The work coordinator saw parallel-agent queue pressure in the \(window)."
        default:
            why = "Calibration suggests the \(displayName(profile).lowercased()) work style."
        }
        let evidence = signals.isEmpty ? "\(ops) operations." : "\(ops) operations; \(signals.joined(separator: ", "))."
        return "\(why) \(evidence)"
    }

    static func tradeoffText(profile: String, policy: PressurePolicy?) -> String {
        if profile == multiAgentSoft {
            if weakensProtection(profile: profile, policy: policy) {
                return "Applying turns off launch blocking and auto-shed. New agent sessions keep launching; the coordinator only warns earlier when the work queue piles up."
            }
            return "Applying keeps observe-only protection and warns earlier when the work queue piles up."
        }
        return "Apply this named work style? Existing leases keep running."
    }

    static func evidenceBits(_ calibration: WorkCalibration) -> [String] {
        let signals = calibration.reviewSignals
        var bits: [String] = []
        func add(_ count: Int?, _ label: String) {
            guard let count, count > 0 else { return }
            bits.append("\(count) \(label)")
        }
        add(signals?.longWaitOperations, "long waits")
        add(signals?.reservationDeferrals, "reservation deferrals")
        add(signals?.wrapperInterruptOperations ?? calibration.wrapperInterruptOperations, "wrapper interrupts")
        add(signals?.cancelledOperations, "cancels")
        add(signals?.singleflightWaits, "singleflight waits")
        add(signals?.bypassedAdmissions, "bypassed admissions")
        return bits
    }

    static func agentPaste(
        profile: String,
        reason: String,
        currentTitle: String,
        currentFlags: String,
        explanation: String,
        tradeoff: String,
        calibration: WorkCalibration?,
        policy: PressurePolicy?,
        apply: String,
        dryRun: String
    ) -> String {
        var lines = [
            "Session Pressure suggestion (advisory; do not apply unless asked)",
            "Current profile: \(currentTitle)\(currentFlags.isEmpty ? "" : " (\(currentFlags))")",
            "Suggested profile: \(profile)",
        ]
        if !reason.isEmpty {
            lines.append("Reason code: \(reason)")
        }
        if let ops = calibration?.operationCount {
            lines.append("Operations (24h): \(ops)")
        }
        if let policy {
            lines.append("enforce_admission=\(policy.enforceAdmission) auto_shed_critical=\(policy.autoShedCritical)")
        }
        lines.append(explanation)
        lines.append(tradeoff)
        lines.append("Dry-run: \(dryRun)")
        lines.append("Apply: \(apply)")
        return lines.joined(separator: "\n")
    }
}
