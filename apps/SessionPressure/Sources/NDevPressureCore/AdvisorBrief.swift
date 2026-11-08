import Foundation

/// Read-only projection of `ndev --json advisor brief show`.
///
/// The advisor owns ranking and day state. The pressure app only decodes the
/// compact contract so it can show the existing day gate without becoming a
/// second planner.
public struct AdvisorBrief: Codable, Equatable, Sendable {
    public static let schemaV1 = "nicos.advisor.brief.v1"

    public var schema: String
    public var day: String
    public var generatedAt: String
    public var gate: String
    public var lanes: AdvisorBriefLanes
    public var byteCount: Int
    public var truncated: Bool
    public var detailHint: String?

    public init(
        schema: String = AdvisorBrief.schemaV1,
        day: String,
        generatedAt: String = "",
        gate: String = "none",
        lanes: AdvisorBriefLanes = .init(),
        byteCount: Int = 0,
        truncated: Bool = false,
        detailHint: String? = nil
    ) {
        self.schema = schema
        self.day = day
        self.generatedAt = generatedAt
        self.gate = gate
        self.lanes = lanes
        self.byteCount = byteCount
        self.truncated = truncated
        self.detailHint = detailHint
    }

    public var residueCount: Int { lanes.residue.count }

    /// Badge text keeps residue visible without hiding the host-pressure state.
    public func dockBadge(for pressure: PressureLevel) -> String? {
        let residue = residueCount > 0 ? "R\(min(residueCount, 99))" : nil
        switch pressure {
        case .normal, .unknown:
            return residue
        case .warning:
            return ["!", residue].compactMap { $0 }.joined(separator: "·")
        case .red:
            return ["RED", residue].compactMap { $0 }.joined(separator: "·")
        case .critical:
            return ["CRIT", residue].compactMap { $0 }.joined(separator: "·")
        }
    }

    private enum CodingKeys: String, CodingKey {
        case schema, day, gate, lanes, truncated
        case generatedAt = "generated_at"
        case byteCount = "byte_count"
        case detailHint = "detail_hint"
    }

    public init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        schema = try values.decodeIfPresent(String.self, forKey: .schema) ?? ""
        day = try values.decodeIfPresent(String.self, forKey: .day) ?? ""
        generatedAt = try values.decodeIfPresent(String.self, forKey: .generatedAt) ?? ""
        gate = try values.decodeIfPresent(String.self, forKey: .gate) ?? "none"
        lanes = try values.decodeIfPresent(AdvisorBriefLanes.self, forKey: .lanes) ?? .init()
        byteCount = try values.decodeIfPresent(Int.self, forKey: .byteCount) ?? 0
        truncated = try values.decodeIfPresent(Bool.self, forKey: .truncated) ?? false
        detailHint = try values.decodeIfPresent(String.self, forKey: .detailHint)
    }
}

public struct AdvisorBriefLanes: Codable, Equatable, Sendable {
    public var residue: [AdvisorBriefItem]
    public var moved: [AdvisorBriefItem]
    public var spine: [AdvisorBriefItem]
    public var eligible: [AdvisorBriefItem]
    public var parked: [AdvisorBriefItem]
    public var doNotTouch: [AdvisorBriefItem]

    public init(
        residue: [AdvisorBriefItem] = [],
        moved: [AdvisorBriefItem] = [],
        spine: [AdvisorBriefItem] = [],
        eligible: [AdvisorBriefItem] = [],
        parked: [AdvisorBriefItem] = [],
        doNotTouch: [AdvisorBriefItem] = []
    ) {
        self.residue = residue
        self.moved = moved
        self.spine = spine
        self.eligible = eligible
        self.parked = parked
        self.doNotTouch = doNotTouch
    }

    private enum CodingKeys: String, CodingKey {
        case residue, moved, spine, eligible, parked
        case doNotTouch = "do_not_touch"
    }

    public init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        residue = try values.decodeIfPresent([AdvisorBriefItem].self, forKey: .residue) ?? []
        moved = try values.decodeIfPresent([AdvisorBriefItem].self, forKey: .moved) ?? []
        spine = try values.decodeIfPresent([AdvisorBriefItem].self, forKey: .spine) ?? []
        eligible = try values.decodeIfPresent([AdvisorBriefItem].self, forKey: .eligible) ?? []
        parked = try values.decodeIfPresent([AdvisorBriefItem].self, forKey: .parked) ?? []
        doNotTouch = try values.decodeIfPresent([AdvisorBriefItem].self, forKey: .doNotTouch) ?? []
    }
}

public struct AdvisorBriefItem: Codable, Equatable, Identifiable, Sendable {
    public var id: String
    public var kind: String?
    public var title: String?
    public var nextPrompt: String?
    public var reason: String?

    public init(
        id: String,
        kind: String? = nil,
        title: String? = nil,
        nextPrompt: String? = nil,
        reason: String? = nil
    ) {
        self.id = id
        self.kind = kind
        self.title = title
        self.nextPrompt = nextPrompt
        self.reason = reason
    }

    private enum CodingKeys: String, CodingKey {
        case id, kind, title, reason
        case nextPrompt = "next_prompt"
    }
}
