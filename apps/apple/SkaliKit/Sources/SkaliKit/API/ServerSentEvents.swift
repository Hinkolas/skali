import Foundation

/// One event of a server-sent event stream: its name and its data.
public struct ServerSentEvent: Hashable, Sendable {
    public let name: String
    public let data: String

    public init(name: String, data: String) {
        self.name = name
        self.data = data
    }
}

/// Reads the daemon's event streams line by line. The daemon writes every
/// event as `event:` then a single `data:` line (its JSON is one line), so
/// an event is complete at its data line; the blank line that ends it in
/// the format is not needed, which matters because line sequences skip
/// empty lines. Comments (`: ping`) and `id:` lines are ignored.
struct ServerSentEventParser {
    private var name = "message"

    mutating func consume(_ line: Substring) -> ServerSentEvent? {
        if line.isEmpty || line.hasPrefix(":") {
            if line.isEmpty { name = "message" }
            return nil
        }
        let field: Substring
        var value: Substring
        if let colon = line.firstIndex(of: ":") {
            field = line[..<colon]
            value = line[line.index(after: colon)...]
            if value.hasPrefix(" ") { value = value.dropFirst() }
        } else {
            field = line
            value = ""
        }
        switch field {
        case "event":
            name = String(value)
        case "data":
            defer { name = "message" }
            return ServerSentEvent(name: name, data: String(value))
        default:
            break
        }
        return nil
    }
}

extension APIClient {
    /// Follows an event stream until the daemon closes it, it fails, or the
    /// consuming task is cancelled. Reconnecting is the caller's call:
    /// every stream of the daemon starts with a full document (or a
    /// `ready` signal), so a new connection never needs a cursor.
    public func events(_ path: String) -> AsyncThrowingStream<ServerSentEvent, Error> {
        let request = makeRequest("GET", path, accept: "text/event-stream")
        let session = session
        return AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    let (bytes, response) = try await session.bytes(for: request)
                    guard let http = response as? HTTPURLResponse else {
                        throw APIError.transport("The cluster sent no HTTP response.")
                    }
                    guard (200..<300).contains(http.statusCode) else {
                        var body = Data()
                        for try await byte in bytes.prefix(16 * 1024) { body.append(byte) }
                        throw Self.failure(status: http.statusCode, body: body)
                    }
                    var parser = ServerSentEventParser()
                    for try await line in bytes.lines {
                        if let event = parser.consume(line[...]) { continuation.yield(event) }
                    }
                    continuation.finish()
                } catch let error as APIError {
                    continuation.finish(throwing: error)
                } catch let error as URLError where error.code == .cancelled {
                    continuation.finish()
                } catch is CancellationError {
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: APIError.transport(error.localizedDescription))
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }
}

extension AsyncThrowingStream where Failure == Error {
    /// The events of `source` decoded as `Element`, skipping events with
    /// other names and data that does not decode.
    static func decoding(
        _ source: AsyncThrowingStream<ServerSentEvent, Error>, event: String
    ) -> AsyncThrowingStream<Element, Error> where Element: Decodable & Sendable {
        AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    for try await message in source where message.name == event {
                        if let value = try? JSONCoding.decoder.decode(Element.self, from: Data(message.data.utf8)) {
                            continuation.yield(value)
                        }
                    }
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }
}
