import Darwin
import Foundation

/// Owner-only HTTP/1.0 transport. One socket, one absolute deadline, bounded
/// headers and body, no redirects, and no shell or helper process.
enum UnixHTTP {
    static let maximumBody = 2 * 1024 * 1024

    static func request(path: String, target: String, method: String, body: Data?, token: String?, timeout: TimeInterval) throws -> (Data, Int) {
        var address = sockaddr_un()
        address.sun_family = sa_family_t(AF_UNIX)
        let bytes = Array(path.utf8) + [0]
        guard bytes.count <= MemoryLayout.size(ofValue: address.sun_path), path.hasPrefix("/"),
              !target.contains("\r"), !target.contains("\n"),
              token?.contains(where: { $0 == "\r" || $0 == "\n" }) != true,
              (body?.count ?? 0) <= 64 * 1024 else {
            throw NDevPressureAPIError.malformed("invalid bounded Unix request")
        }
        var info = stat()
        guard lstat(path, &info) == 0, info.st_mode & S_IFMT == S_IFSOCK,
              info.st_uid == getuid(), info.st_mode & 0o077 == 0 else {
            throw NDevPressureAPIError.unavailable("owner-only socket is absent")
        }
        address.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)
        withUnsafeMutableBytes(of: &address.sun_path) { buffer in buffer.copyBytes(from: bytes) }
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw NDevPressureAPIError.unavailable("could not create Unix socket") }
        defer { Darwin.close(fd) }
        var noSignal: Int32 = 1
        _ = setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &noSignal, socklen_t(MemoryLayout<Int32>.size))
        guard fcntl(fd, F_SETFL, O_NONBLOCK) == 0 else { throw NDevPressureAPIError.unavailable("could not bound Unix socket") }
        let deadline = ProcessInfo.processInfo.systemUptime + timeout
        func wait(_ events: Int16) throws {
            while true {
                if Task.isCancelled { throw CancellationError() }
                let left = deadline - ProcessInfo.processInfo.systemUptime
                guard left > 0 else { throw NDevPressureAPIError.unavailable("Unix request deadline exceeded") }
                var descriptor = pollfd(fd: fd, events: events, revents: 0)
                let result = poll(&descriptor, 1, Int32(min(left * 1000, 100)))
                if result > 0 { return }
                if result < 0 && errno != EINTR { throw NDevPressureAPIError.unavailable("Unix socket poll failed") }
            }
        }
        let connected = withUnsafePointer(to: &address) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
        }
        if connected != 0 {
            guard errno == EINPROGRESS else { throw NDevPressureAPIError.unavailable("Unix API is not listening") }
            try wait(Int16(POLLOUT))
            var error: Int32 = 0
            var length = socklen_t(MemoryLayout<Int32>.size)
            guard getsockopt(fd, SOL_SOCKET, SO_ERROR, &error, &length) == 0, error == 0 else {
                throw NDevPressureAPIError.unavailable("Unix API connect failed")
            }
        }
        var header = "\(method) \(target) HTTP/1.0\r\nHost: localhost\r\nAccept: application/json\r\nConnection: close\r\n"
        if let token { header += "Authorization: Bearer \(token)\r\n" }
        if let body { header += "Content-Type: application/json\r\nContent-Length: \(body.count)\r\n" }
        var request = Data((header + "\r\n").utf8)
        if let body { request.append(body) }
        var offset = 0
        while offset < request.count {
            try wait(Int16(POLLOUT))
            let sent = request.withUnsafeBytes { Darwin.send(fd, $0.baseAddress!.advanced(by: offset), request.count - offset, 0) }
            if sent > 0 { offset += sent }
            else if sent == 0 || (errno != EAGAIN && errno != EINTR) { throw NDevPressureAPIError.unavailable("Unix send failed") }
        }
        var received = Data()
        var buffer = [UInt8](repeating: 0, count: 8192)
        var headerEnd: Int?
        while true {
            try wait(Int16(POLLIN))
            let count = recv(fd, &buffer, buffer.count, 0)
            if count == 0 { break }
            if count < 0 {
                if errno == EAGAIN || errno == EINTR { continue }
                throw NDevPressureAPIError.unavailable("Unix receive failed")
            }
            guard received.count + count <= maximumBody + 64 * 1024 else { throw NDevPressureAPIError.malformed("response exceeds 2 MiB") }
            received.append(contentsOf: buffer.prefix(count))
            if headerEnd == nil { headerEnd = received.range(of: Data("\r\n\r\n".utf8))?.upperBound }
            if headerEnd == nil && received.count > 64 * 1024 { throw NDevPressureAPIError.malformed("oversized HTTP headers") }
        }
        guard let end = headerEnd, received.count - end <= maximumBody,
              let headers = String(data: received.prefix(end), encoding: .utf8),
              let line = headers.components(separatedBy: "\r\n").first,
              let status = Int(line.split(separator: " ").dropFirst().first ?? ""),
              !headers.lowercased().contains("transfer-encoding:") else {
            throw NDevPressureAPIError.malformed("invalid Unix HTTP response")
        }
        let payload = received.subdata(in: end..<received.count)
        for line in headers.components(separatedBy: "\r\n") where line.lowercased().hasPrefix("content-length:") {
            guard let length = Int(line.dropFirst(15).trimmingCharacters(in: .whitespaces)), length == payload.count else {
                throw NDevPressureAPIError.malformed("incomplete HTTP response")
            }
        }
        return (payload, status)
    }
}
