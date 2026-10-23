import Foundation

/// An error the client surfaces: the daemon's error envelope
/// (`{"error":{"code","message"}}`), or a transport or decoding failure.
public enum APIError: Error, Sendable, Equatable {
    /// The daemon answered with its error envelope.
    case server(status: Int, code: String, message: String)
    /// A non-2xx response without a readable envelope.
    case http(status: Int)
    /// The response was not what the client expected.
    case decoding(String)
    /// The request never got an answer.
    case transport(String)

    public var status: Int? {
        switch self {
        case .server(let status, _, _), .http(let status): status
        case .decoding, .transport: nil
        }
    }

    public var code: String? {
        if case .server(_, let code, _) = self { code } else { nil }
    }

    /// The session is gone (expired, revoked, or the cluster was reset).
    public var isUnauthorized: Bool { status == 401 }

    /// The route wants a recent sign-in (sudo mode) before it acts.
    public var isReauthRequired: Bool { status == 403 && code == "reauth_required" }
}

extension APIError: LocalizedError {
    public var errorDescription: String? {
        switch self {
        case .server(_, _, let message): message
        case .http(let status): "The cluster answered with HTTP \(status)."
        case .decoding(let detail): "The cluster sent an unexpected response (\(detail))."
        case .transport(let detail): detail
        }
    }
}

struct ErrorEnvelope: Decodable {
    struct Detail: Decodable {
        let code: String
        let message: String
    }
    let error: Detail
}
