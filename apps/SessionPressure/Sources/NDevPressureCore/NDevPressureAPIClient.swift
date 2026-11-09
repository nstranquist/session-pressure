import Foundation

public enum NDevPressureAPIError: Error, LocalizedError, Sendable {
    case unavailable(String)
    case server(status: Int, code: String, message: String)
    case malformed(String)

    public var errorDescription: String? {
        switch self {
        case .unavailable(let detail):
            return "SessionPressure local API unavailable: \(detail)"
        case .server(let status, let code, let message):
            return "SessionPressure API HTTP \(status) \(code): \(message)"
        case .malformed(let detail):
            return "Malformed SessionPressure API response: \(detail)"
        }
    }

    var permitsCLIFallback: Bool {
        if case .unavailable = self { return true }
        return false
    }
}

/// Small local client for the explicit SessionPressure API. The Unix socket
/// is the default transport; URLSession is used only when an
/// operator explicitly configures NDEV_PRESSURE_API_URL, with the existing CLI
/// path as the bounded fallback for an unavailable endpoint.
public struct NDevPressureAPIClient: Sendable {
    private static let apiVersion = "nicos.session.pressure.control.v1"
    private let baseURL: URL
    private let socketPath: String?
    private let token: String?
    private let timeout: TimeInterval

    /// Which transport the app will use, and why. One resolver feeds both the
    /// client and the Settings page so the page never guesses.
    public enum Transport: Sendable, Equatable {
        case socket(path: String)
        case url(String)
        case cliFallback(reason: String)

        public var summary: String {
            switch self {
            case .socket(let path): "Local API socket · \(path)"
            case .url(let url): "Local API URL · \(url)"
            case .cliFallback(let reason): "CLI only · \(reason)"
            }
        }
    }

    public static func transport(environment: [String: String] = ProcessInfo.processInfo.environment) -> Transport {
        let configuredURL = environment["NDEV_PRESSURE_API_URL"]
        let home = environment["HOME"] ?? FileManager.default.homeDirectoryForCurrentUser.path
        let state = environment["NDEV_SESSION_PRESSURE_HOME"] ?? environment["SESSION_PRESSURE_HOME"] ?? "\(home)/.nicos-dev/session-pressure"
        let socket = environment["NDEV_PRESSURE_API_SOCKET"] ?? "\(state)/api/session-pressure.sock"
        if let configuredURL {
            guard let url = URL(string: configuredURL),
                  let scheme = url.scheme?.lowercased(),
                  scheme == "http" || scheme == "https",
                  let host = url.host?.lowercased(),
                  ["localhost", "127.0.0.1", "::1"].contains(host),
                  url.user == nil,
                  url.password == nil,
                  url.query == nil,
                  url.fragment == nil else {
                return .cliFallback(reason: "API URL is not a plain loopback http(s) URL")
            }
            return .url(configuredURL)
        }
        // Default to the local socket only if it exists. CLI fallback stays
        // cheap when the optional API service is not installed.
        guard FileManager.default.fileExists(atPath: socket) else {
            return .cliFallback(reason: "no API socket at \(socket)")
        }
        return .socket(path: socket)
    }

    public init?(environment: [String: String] = ProcessInfo.processInfo.environment) {
        switch Self.transport(environment: environment) {
        case .cliFallback:
            return nil
        case .url(let raw):
            guard let url = URL(string: raw) else { return nil }
            self.baseURL = url
            self.socketPath = nil
        case .socket(let path):
            self.baseURL = URL(string: "http://localhost")!
            self.socketPath = path
        }
        self.timeout = min(max(Double(environment["NDEV_PRESSURE_API_TIMEOUT_SECONDS"] ?? "10") ?? 10, 1), 30)
        if let inline = environment["NDEV_PRESSURE_API_TOKEN"], !inline.isEmpty {
            self.token = inline
        } else if let path = environment["NDEV_PRESSURE_API_TOKEN_FILE"],
                  let data = FileManager.default.contents(atPath: path),
                  let value = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines),
                  !value.isEmpty {
            self.token = value
        } else {
            self.token = nil
        }
    }

    public func projection<T: Decodable>(
        _ route: String,
        query: [String: String] = [:],
        as type: T.Type
    ) async throws -> T {
        try await request(route, query: query, body: nil, as: type)
    }

    public func action<T: Decodable>(
        _ route: String,
        body: Encodable,
        as type: T.Type
    ) async throws -> T {
        let data = try JSONEncoder().encode(AnyEncodable(body))
        return try await request(route, query: [:], body: data, as: type, method: "POST")
    }

    private func request<T: Decodable>(
        _ route: String,
        query: [String: String],
        body: Data?,
        as type: T.Type,
        method: String = "GET"
    ) async throws -> T {
        var components = URLComponents(url: baseURL, resolvingAgainstBaseURL: false)
        let basePath = components?.path.trimmingCharacters(in: CharacterSet(charactersIn: "/")) ?? ""
        let routePath = route.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        components?.path = "/" + ([basePath, routePath].filter { !$0.isEmpty }.joined(separator: "/"))
        components?.queryItems = query.isEmpty ? nil : query.keys.sorted().map { URLQueryItem(name: $0, value: query[$0]) }
        guard let url = components?.url else { throw NDevPressureAPIError.unavailable("invalid endpoint") }

        var request = URLRequest(url: url, timeoutInterval: timeout)
        request.httpMethod = method
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let token { request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        if let body {
            request.httpBody = body
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }

        let data: Data
        let status: Int
        if let socketPath {
            let target = (components?.percentEncodedPath.isEmpty == false ? components!.percentEncodedPath : "/") + (components?.percentEncodedQuery.map { "?" + $0 } ?? "")
            let task = Task.detached {
                try UnixHTTP.request(path: socketPath, target: target, method: method, body: body, token: token, timeout: timeout)
            }
            (data, status) = try await withTaskCancellationHandler(operation: { try await task.value }, onCancel: { task.cancel() })
        } else {
            do {
                let configuration = URLSessionConfiguration.ephemeral
                configuration.timeoutIntervalForRequest = timeout
                configuration.timeoutIntervalForResource = timeout
                let session = URLSession(configuration: configuration, delegate: LocalOnlyRedirectDelegate(), delegateQueue: nil)
                defer { session.invalidateAndCancel() }
                let (stream, response) = try await session.bytes(for: request)
                guard let http = response as? HTTPURLResponse else {
                    throw NDevPressureAPIError.unavailable("non-HTTP response")
                }
                status = http.statusCode
                var bytes = Data()
                for try await byte in stream {
                    guard bytes.count < 2 * 1024 * 1024 else { throw NDevPressureAPIError.malformed("response exceeds 2 MiB") }
                    bytes.append(byte)
                }
                data = bytes
            } catch let error as NDevPressureAPIError { throw error }
            catch { throw NDevPressureAPIError.unavailable(error.localizedDescription) }
        }
        let envelope: APIEnvelope<T>
        do {
            envelope = try PressureJSON.decode(APIEnvelope<T>.self, from: data)
        } catch {
            throw NDevPressureAPIError.malformed(String(describing: error))
        }
        guard envelope.apiVersion == Self.apiVersion else {
            throw NDevPressureAPIError.malformed("unsupported API version \(envelope.apiVersion)")
        }
        if let error = envelope.error {
            throw NDevPressureAPIError.server(status: status, code: error.code, message: error.message)
        }
        guard (200..<300).contains(status), let payload = envelope.data else {
            throw NDevPressureAPIError.server(status: status, code: "http_error", message: "request failed")
        }
        return payload
    }
}

private struct APIEnvelope<T: Decodable>: Decodable {
    enum CodingKeys: String, CodingKey {
        case apiVersion = "api_version"
        case data, error
    }
    let apiVersion: String
    let data: T?
    let error: APIError?
}

private struct APIError: Decodable {
    let code: String
    let message: String
}

private struct AnyEncodable: Encodable {
    private let encodeValue: (Encoder) throws -> Void

    init(_ value: Encodable) {
        self.encodeValue = value.encode(to:)
    }

    func encode(to encoder: Encoder) throws {
        try encodeValue(encoder)
    }
}

private final class LocalOnlyRedirectDelegate: NSObject, URLSessionTaskDelegate, Sendable {
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest, completionHandler: @escaping @Sendable (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}
