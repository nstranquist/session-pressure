import Foundation

public enum PressureJSON {
    public static let decoder: JSONDecoder = {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { decoder in
            let container = try decoder.singleValueContainer()
            if let seconds = try? container.decode(Double.self) {
                return Date(timeIntervalSince1970: seconds)
            }
            let string = try container.decode(String.self)
            if let date = parseISO8601(string) {
                return date
            }
            throw DecodingError.dataCorruptedError(
                in: container,
                debugDescription: "Unrecognized date: \(string)"
            )
        }
        return decoder
    }()

    /// Thread-local ISO parsing without shared mutable formatters.
    public static func parseISO8601(_ string: String) -> Date? {
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = fractional.date(from: string) {
            return date
        }
        let basic = ISO8601DateFormatter()
        basic.formatOptions = [.withInternetDateTime]
        if let date = basic.date(from: string) {
            return date
        }
        // Collapse over-long fractional seconds for DateFormatter fallback.
        let loose = DateFormatter()
        loose.locale = Locale(identifier: "en_US_POSIX")
        loose.timeZone = TimeZone(secondsFromGMT: 0)
        loose.dateFormat = "yyyy-MM-dd'T'HH:mm:ss.SSSSSSZ"
        let normalized = normalizeFractional(string.replacingOccurrences(of: "Z", with: "+0000"))
        return loose.date(from: normalized)
    }

    private static func normalizeFractional(_ value: String) -> String {
        guard let dot = value.firstIndex(of: ".") else { return value }
        let head = value[..<dot]
        let tail = value[value.index(after: dot)...]
        var digits = ""
        var suffix = ""
        for ch in tail {
            if ch.isNumber {
                if digits.count < 6 { digits.append(ch) }
            } else {
                suffix = String(tail[tail.firstIndex(of: ch)!...])
                break
            }
        }
        while digits.count < 6 { digits.append("0") }
        return "\(head).\(digits)\(suffix)"
    }

    public static func decode<T: Decodable>(_ type: T.Type, from data: Data) throws -> T {
        try decoder.decode(type, from: data)
    }
}
