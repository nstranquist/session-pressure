import Foundation

/// Closed argv only — never shell text or a named operator provider on auto_safe.
public enum StorageReclaim {
    public enum Tier: String, Sendable, Hashable {
        case autoSafe = "auto_safe"
        case operatorProvider = "operator"
        case reportOnly = "report_only"
        case unknown
    }

    public static func tier(classification: String) -> Tier {
        switch classification.lowercased() {
        case "auto_safe": return .autoSafe
        case "operator": return .operatorProvider
        case "report_only": return .reportOnly
        default: return .unknown
        }
    }

    public static func isFactoryOnly(_ provider: StorageProviderReport) -> Bool {
        let reason = provider.blockedReason?.lowercased() ?? ""
        return reason.contains("pageskein") || reason.contains("not available in the open extract")
    }

    public static func isActionable(_ provider: StorageProviderReport) -> Bool {
        provider.mutationSupported && !provider.activeOwner && (provider.blockedReason ?? "").isEmpty &&
            (tier(classification: provider.classification) == .autoSafe || tier(classification: provider.classification) == .operatorProvider)
    }

    public static func applyArguments(autoSafe: Bool, provider: String?, apply: Bool, targetFree: String? = nil, force: Bool = false) -> [String] {
        var args = ["--json", "session", "pressure", "storage", "apply"]
        if autoSafe {
            args.append("--auto-safe")
        } else if let provider, StorageProviderID.isSafe(provider) {
            args.append(contentsOf: ["--provider", provider])
            if force {
                args.append("--force")
            }
        }
        if let targetFree, StorageTargetFree.isSafe(targetFree) {
            args.append(contentsOf: ["--target-free", targetFree])
        }
        if apply {
            args.append("--apply")
        }
        return args
    }

    public static func receiptLines(from envelope: StorageApplyEnvelope, command: [String]) -> [StorageReceiptLine] {
        var lines: [StorageReceiptLine] = []
        lines.append(StorageReceiptLine(kind: .command, text: command.joined(separator: " ")))
        if envelope.apply == true {
            lines.append(StorageReceiptLine(kind: .status, text: "Typed apply running. Mutation stays on --auto-safe or a named --provider."))
        } else {
            lines.append(StorageReceiptLine(kind: .status, text: "Preview only. apply=false — no host bytes were deleted."))
        }
        if envelope.autoSafe == true {
            lines.append(StorageReceiptLine(kind: .status, text: "Typed target: --auto-safe (closed class)."))
        } else if let provider = envelope.selectedProvider, !provider.isEmpty {
            lines.append(StorageReceiptLine(kind: .status, text: "Typed target: --provider \(provider)."))
        }
        if let error = envelope.error, !error.isEmpty {
            lines.append(StorageReceiptLine(kind: .error, text: error))
        }
        if let plan = envelope.plan {
            for provider in plan.providers {
                lines.append(StorageReceiptLine(kind: .provider, text: providerLine(provider)))
            }
        }
        if let skipped = envelope.skippedProviders {
            for row in skipped {
                let id = row.providerID ?? "provider"
                let reason = row.reason ?? "skipped"
                lines.append(StorageReceiptLine(kind: .blocked, text: "skipped \(id): \(reason)"))
            }
        }
        if let receipts = envelope.receipts {
            for receipt in receipts {
                lines.append(StorageReceiptLine(kind: .result, text: resultLine(receipt)))
            }
        }
        return lines
    }

    public static func providerLine(_ provider: StorageProviderReport) -> String {
        var parts = ["\(provider.classification) \(provider.id)"]
        if isFactoryOnly(provider) {
            parts.append("factory-only")
        }
        if let reason = provider.blockedReason, !reason.isEmpty {
            parts.append("blocked: \(reason)")
        } else if isActionable(provider) {
            parts.append("actionable")
        } else {
            parts.append("non-actionable")
        }
        if provider.estimatedBytes > 0 {
            parts.append("est \(PressureFormat.bytes(provider.estimatedBytes))")
        }
        return parts.joined(separator: " · ")
    }

    private static func resultLine(_ receipt: StorageApplyReceipt) -> String {
        var parts = ["receipt \(receipt.providerID ?? "provider")"]
        if let mode = receipt.mode { parts.append(mode) }
        if let outcome = receipt.outcome { parts.append(outcome) }
        if let bytes = receipt.reclaimedBytes, bytes > 0 {
            parts.append("reclaimed \(PressureFormat.bytes(bytes))")
        }
        if let error = receipt.error, !error.isEmpty {
            parts.append(error)
        }
        return parts.joined(separator: " · ")
    }
}

public enum StorageProviderID {
    public static func isSafe(_ value: String) -> Bool {
        let count = value.utf8.count
        guard (1...80).contains(count) else { return false }
        return value.unicodeScalars.allSatisfy { scalar in
            CharacterSet.alphanumerics.contains(scalar) || "._:-".unicodeScalars.contains(scalar)
        }
    }
}

public enum StorageTargetFree {
    public static func isSafe(_ value: String) -> Bool {
        let suffixes = ["GiB", "GB", "MiB", "MB", "KiB", "KB", "B", ""]
        for suffix in suffixes {
            guard value.hasSuffix(suffix) else { continue }
            let number = String(value.dropLast(suffix.count))
            if !number.isEmpty, number.allSatisfy(\.isNumber) {
                return true
            }
        }
        return false
    }
}

public struct StorageReceiptLine: Sendable, Hashable, Identifiable {
    public enum Kind: String, Sendable, Hashable {
        case command
        case stdout
        case status
        case provider
        case blocked
        case result
        case error
    }

    public var id: String
    public var kind: Kind
    public var text: String

    public init(kind: Kind, text: String, id: String = UUID().uuidString) {
        self.id = id
        self.kind = kind
        self.text = text
    }
}

public struct StorageProviderReport: Codable, Sendable, Hashable, Identifiable {
    public var id: String
    public var classification: String
    public var summary: String
    public var path: String?
    public var present: Bool
    public var estimatedBytes: Int64
    public var estimateKind: String?
    public var mutationSupported: Bool
    public var activeOwner: Bool
    public var blockedReason: String?

    enum CodingKeys: String, CodingKey {
        case id
        case classification
        case summary
        case path
        case present
        case estimatedBytes = "estimated_bytes"
        case estimateKind = "estimate_kind"
        case mutationSupported = "mutation_supported"
        case activeOwner = "active_owner"
        case blockedReason = "blocked_reason"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decodeIfPresent(String.self, forKey: .id) ?? ""
        classification = try c.decodeIfPresent(String.self, forKey: .classification) ?? ""
        summary = try c.decodeIfPresent(String.self, forKey: .summary) ?? ""
        path = try c.decodeIfPresent(String.self, forKey: .path)
        present = try c.decodeIfPresent(Bool.self, forKey: .present) ?? false
        estimatedBytes = try c.decodeIfPresent(Int64.self, forKey: .estimatedBytes) ?? 0
        estimateKind = try c.decodeIfPresent(String.self, forKey: .estimateKind)
        mutationSupported = try c.decodeIfPresent(Bool.self, forKey: .mutationSupported) ?? false
        activeOwner = try c.decodeIfPresent(Bool.self, forKey: .activeOwner) ?? false
        blockedReason = try c.decodeIfPresent(String.self, forKey: .blockedReason)
    }

    public init(
        id: String,
        classification: String,
        summary: String = "",
        mutationSupported: Bool = false,
        activeOwner: Bool = false,
        blockedReason: String? = nil,
        estimatedBytes: Int64 = 0,
        present: Bool = false
    ) {
        self.id = id
        self.classification = classification
        self.summary = summary
        self.mutationSupported = mutationSupported
        self.activeOwner = activeOwner
        self.blockedReason = blockedReason
        self.estimatedBytes = estimatedBytes
        self.present = present
    }

    public var tier: StorageReclaim.Tier { StorageReclaim.tier(classification: classification) }
    public var isActionable: Bool { StorageReclaim.isActionable(self) }
    public var isFactoryOnly: Bool { StorageReclaim.isFactoryOnly(self) }
}

public struct StoragePolicySnapshot: Codable, Sendable, Hashable {
    public var enabled: Bool
    public var enforceAdmission: Bool
    public var targetFreeBytes: Int64

    enum CodingKeys: String, CodingKey {
        case enabled
        case enforceAdmission = "enforce_admission"
        case targetFreeBytes = "target_free_bytes"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? false
        enforceAdmission = try c.decodeIfPresent(Bool.self, forKey: .enforceAdmission) ?? false
        targetFreeBytes = try c.decodeIfPresent(Int64.self, forKey: .targetFreeBytes) ?? 0
    }

    public init(enabled: Bool = false, enforceAdmission: Bool = false, targetFreeBytes: Int64 = 0) {
        self.enabled = enabled
        self.enforceAdmission = enforceAdmission
        self.targetFreeBytes = targetFreeBytes
    }
}

public struct StoragePlan: Codable, Sendable, Hashable {
    public var sample: StorageSnapshot?
    public var targetFreeBytes: Int64
    public var shortfallBytes: Int64
    public var estimatedReclaimableBytes: Int64
    public var providers: [StorageProviderReport]

    enum CodingKeys: String, CodingKey {
        case sample
        case targetFreeBytes = "target_free_bytes"
        case shortfallBytes = "shortfall_bytes"
        case estimatedReclaimableBytes = "estimated_reclaimable_bytes"
        case providers
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        sample = try c.decodeIfPresent(StorageSnapshot.self, forKey: .sample)
        targetFreeBytes = try c.decodeIfPresent(Int64.self, forKey: .targetFreeBytes) ?? 0
        shortfallBytes = try c.decodeIfPresent(Int64.self, forKey: .shortfallBytes) ?? 0
        estimatedReclaimableBytes = try c.decodeIfPresent(Int64.self, forKey: .estimatedReclaimableBytes) ?? 0
        providers = try c.decodeIfPresent([StorageProviderReport].self, forKey: .providers) ?? []
    }
}

public struct StorageSkippedProvider: Codable, Sendable, Hashable {
    public var providerID: String?
    public var reason: String?

    enum CodingKeys: String, CodingKey {
        case providerID = "provider_id"
        case reason
    }
}

public struct StorageApplyReceipt: Codable, Sendable, Hashable {
    public var providerID: String?
    public var mode: String?
    public var outcome: String?
    public var reclaimedBytes: Int64?
    public var error: String?

    enum CodingKeys: String, CodingKey {
        case providerID = "provider_id"
        case mode
        case outcome
        case reclaimedBytes = "reclaimed_bytes"
        case error
    }
}

public struct StorageApplyEnvelope: Codable, Sendable, Hashable {
    public var ok: Bool?
    public var action: String?
    public var apply: Bool?
    public var autoSafe: Bool?
    public var selectedProvider: String?
    public var plan: StoragePlan?
    public var storage: StorageSnapshot?
    public var receipts: [StorageApplyReceipt]?
    public var skippedProviders: [StorageSkippedProvider]?
    public var error: String?

    enum CodingKeys: String, CodingKey {
        case ok, action, apply, plan, storage, receipts, error
        case autoSafe = "auto_safe"
        case selectedProvider = "selected_provider"
        case skippedProviders = "skipped_providers"
    }
}

public struct StorageProvidersEnvelope: Codable, Sendable, Hashable {
    public var ok: Bool?
    public var action: String?
    public var storage: StorageSnapshot?
    public var providers: [StorageProviderReport]
    public var storagePolicy: StoragePolicySnapshot?

    enum CodingKeys: String, CodingKey {
        case ok, action, storage, providers
        case storagePolicy = "storage_policy"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        ok = try c.decodeIfPresent(Bool.self, forKey: .ok)
        action = try c.decodeIfPresent(String.self, forKey: .action)
        storage = try c.decodeIfPresent(StorageSnapshot.self, forKey: .storage)
        providers = try c.decodeIfPresent([StorageProviderReport].self, forKey: .providers) ?? []
        storagePolicy = try c.decodeIfPresent(StoragePolicySnapshot.self, forKey: .storagePolicy)
    }
}

public struct StorageStatusEnvelope: Codable, Sendable, Hashable {
    public var ok: Bool?
    public var action: String?
    public var storage: StorageSnapshot?
    public var storagePolicy: StoragePolicySnapshot?

    enum CodingKeys: String, CodingKey {
        case ok, action, storage
        case storagePolicy = "storage_policy"
    }
}

public struct StoragePolicyEnvelope: Codable, Sendable, Hashable {
    public var ok: Bool?
    public var action: String?
    public var storagePolicy: StoragePolicySnapshot?
    public var path: String?

    enum CodingKeys: String, CodingKey {
        case ok, action, path
        case storagePolicy = "storage_policy"
    }
}
