import Foundation

/// A database's non-secret connection facts
/// (`GET /v1/environments/{id}/databases/{key}/connection`).
public struct DatabaseConnection: Codable, Hashable, Sendable {
    /// Where the claim stands, such as `Ready` or `Provisioning`.
    public let phase: String
    public let engine: String
    public let major: Int
    public let isolation: String
    public let availability: String
    /// Set once the claim is provisioned.
    public let host: String?
    public let port: Int?
    public let database: String?

    public init(
        phase: String, engine: String, major: Int, isolation: String, availability: String, host: String? = nil,
        port: Int? = nil, database: String? = nil
    ) {
        self.phase = phase
        self.engine = engine
        self.major = major
        self.isolation = isolation
        self.availability = availability
        self.host = host
        self.port = port
        self.database = database
    }

    public var isProvisioned: Bool { host != nil }
}

/// A database's credentials, revealed once and never stored.
public struct DatabaseCredentials: Codable, Hashable, Sendable {
    public let username: String
    public let password: String
    public let url: String

    public init(username: String, password: String, url: String) {
        self.username = username
        self.password = password
        self.url = url
    }
}

/// One line of an application's live log (`RuntimeLogEvent`).
public struct RuntimeLogLine: Codable, Hashable, Sendable {
    public let service: String
    public let pod: String
    public let line: String
    public let time: Date?
    /// From the previous container, after a restart.
    public let previous: Bool?

    public init(service: String, pod: String, line: String, time: Date? = nil, previous: Bool? = nil) {
        self.service = service
        self.pod = pod
        self.line = line
        self.time = time
        self.previous = previous
    }
}
