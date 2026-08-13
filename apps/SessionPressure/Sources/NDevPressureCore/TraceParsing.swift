import Foundation

public enum DiskTraceParser {
    public static func paths(fromFSUsage output: String, maximumPaths: Int = NDevPressureTraceContract.maximumPaths) -> DiskPathTraceResult {
        let limit = min(max(maximumPaths, 1), NDevPressureTraceContract.maximumPaths)
        var seen = Set<String>()
        var paths: [String] = []
        var truncated = false
        for line in output.split(whereSeparator: \.isNewline) {
            guard let path = path(fromFSUsageLine: String(line)), seen.insert(path).inserted else { continue }
            paths.append(path)
            if paths.count == limit {
                truncated = true
                break
            }
        }
        return DiskPathTraceResult(paths: paths, truncated: truncated)
    }

    private static func path(fromFSUsageLine line: String) -> String? {
        guard let slash = line.firstIndex(of: "/") else { return nil }
        var tail = String(line[slash...])
        if let separator = tail.range(of: #"[ \t]{2,}"#, options: .regularExpression) {
            tail = String(tail[..<separator.lowerBound])
        }
        tail = tail.trimmingCharacters(in: .whitespacesAndNewlines)
        guard tail.hasPrefix("/"), !tail.contains("\0"), tail.utf8.count <= 1024 else { return nil }
        return tail
    }
}
