import Foundation

/// An account on a cluster (`User` in api/openapi.yaml).
public struct User: Codable, Hashable, Sendable, Identifiable {
    public let id: String
    public let email: String
    public let name: String
    /// The instance-wide role: `admin` or `member`.
    public let role: String
    public let createProjects: Bool
    public let twoFactorEnabled: Bool
    public let createdAt: Date

    public init(
        id: String, email: String, name: String, role: String,
        createProjects: Bool, twoFactorEnabled: Bool, createdAt: Date
    ) {
        self.id = id
        self.email = email
        self.name = name
        self.role = role
        self.createProjects = createProjects
        self.twoFactorEnabled = twoFactorEnabled
        self.createdAt = createdAt
    }

    public var isAdmin: Bool { role == "admin" }

    /// Up to two initials for the avatar: "Ada Lovelace" becomes "AL".
    public var initials: String {
        let source = name.isEmpty ? email : name
        let letters = source
            .split(whereSeparator: { $0 == " " || $0 == "." || $0 == "@" })
            .prefix(2)
            .compactMap(\.first)
        return String(letters).uppercased()
    }
}

/// One signed-in session (`Session`).
public struct Session: Codable, Hashable, Sendable, Identifiable {
    public let id: String
    public let expiresAt: Date
    public let ipAddress: String?
    public let userAgent: String?
    public let createdAt: Date
    public let current: Bool?
}

/// `GET /v1/auth/session`.
public struct CurrentSession: Codable, Sendable {
    public let user: User
    public let session: Session
}

/// A freshly minted bearer session (`SessionCreated`).
public struct SessionCreated: Codable, Sendable {
    public let token: String
    public let expiresAt: Date
    public let user: User
}

/// `POST /v1/auth/device/requests` (`DeviceRequest`).
public struct DeviceRequest: Codable, Sendable {
    public let deviceCode: String
    public let userCode: String
    public let expiresAt: Date
    /// The minimum seconds between polls.
    public let interval: Int
}

/// `POST /v1/auth/device/token` (`DevicePoll`).
public struct DevicePoll: Codable, Sendable {
    /// `pending`, `approved`, `denied` or `expired`.
    public let status: String
    public let session: SessionCreated?
}
