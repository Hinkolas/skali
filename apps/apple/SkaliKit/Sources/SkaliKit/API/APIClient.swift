import Foundation

/// A thin client for the skalid HTTP API (api/openapi.yaml). It speaks the
/// same bearer-token dialect as the CLI but sends no `Skali-Client-Version`,
/// so the daemon's CLI version gate leaves it alone.
public struct APIClient: Sendable {
    public let address: ClusterAddress
    public let token: String?
    private let session: URLSession

    public init(address: ClusterAddress, token: String? = nil, session: URLSession = .shared) {
        self.address = address
        self.token = token
        self.session = session
    }

    public func authorized(with token: String) -> APIClient {
        APIClient(address: address, token: token, session: session)
    }

    // MARK: Requests

    public func get<Response: Decodable>(_ path: String, query: [URLQueryItem] = []) async throws -> Response {
        try await send("GET", path, query: query, body: Optional<Empty>.none)
    }

    public func post<Body: Encodable, Response: Decodable>(_ path: String, body: Body) async throws -> Response {
        try await send("POST", path, body: body)
    }

    public func post(_ path: String) async throws {
        let _: Empty = try await send("POST", path, body: Optional<Empty>.none)
    }

    public func put<Body: Encodable, Response: Decodable>(_ path: String, body: Body) async throws -> Response {
        try await send("PUT", path, body: body)
    }

    private func send<Body: Encodable, Response: Decodable>(
        _ method: String, _ path: String, query: [URLQueryItem] = [], body: Body?
    ) async throws -> Response {
        var url = address.apiURL.appending(path: path)
        if !query.isEmpty { url.append(queryItems: query) }
        var request = URLRequest(url: url)
        request.httpMethod = method
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let token {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        if let body {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try JSONCoding.encoder.encode(body)
        }

        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch is CancellationError {
            throw CancellationError()
        } catch let error as URLError where error.code == .cancelled {
            throw CancellationError()
        } catch {
            throw APIError.transport(error.localizedDescription)
        }
        guard let http = response as? HTTPURLResponse else {
            throw APIError.transport("The cluster sent no HTTP response.")
        }
        guard (200..<300).contains(http.statusCode) else {
            if let envelope = try? JSONCoding.decoder.decode(ErrorEnvelope.self, from: data) {
                throw APIError.server(
                    status: http.statusCode, code: envelope.error.code, message: envelope.error.message)
            }
            throw APIError.http(status: http.statusCode)
        }
        if Response.self == Empty.self || data.isEmpty {
            if let empty = Empty() as? Response { return empty }
        }
        do {
            return try JSONCoding.decoder.decode(Response.self, from: data)
        } catch {
            throw APIError.decoding(String(describing: error))
        }
    }
}

/// The body of a request or response that carries none.
public struct Empty: Codable, Sendable {
    public init() {}
}

enum JSONCoding {
    static let decoder: JSONDecoder = {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        // Go marshals time.Time as RFC 3339 with optional fractional seconds.
        decoder.dateDecodingStrategy = .custom { decoder in
            let container = try decoder.singleValueContainer()
            let text = try container.decode(String.self)
            if let date = try? Date(text, strategy: .iso8601.year().month().day()
                .time(includingFractionalSeconds: true).timeZone(separator: .colon)) {
                return date
            }
            if let date = try? Date(text, strategy: .iso8601) {
                return date
            }
            throw DecodingError.dataCorruptedError(
                in: container, debugDescription: "Not an RFC 3339 timestamp: \(text)")
        }
        return decoder
    }()

    static let encoder: JSONEncoder = {
        let encoder = JSONEncoder()
        encoder.keyEncodingStrategy = .convertToSnakeCase
        encoder.dateEncodingStrategy = .iso8601
        return encoder
    }()
}
