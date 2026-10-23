import Foundation

/// `GET /v1/system/meta` (`SystemMeta`).
public struct SystemMeta: Codable, Hashable, Sendable {
    public let version: String
    /// The installation name the operator chose at init, when set.
    public let name: String?
    public let instanceId: String?
    public let updateAvailable: Release?

    public init(version: String, name: String?, instanceId: String? = nil, updateAvailable: Release? = nil) {
        self.version = version
        self.name = name
        self.instanceId = instanceId
        self.updateAvailable = updateAvailable
    }
}

/// One published release (`Release`).
public struct Release: Codable, Hashable, Sendable {
    public let version: String
    public let prerelease: Bool?
    public let publishedAt: Date?
    /// The release page (notes and assets).
    public let url: String?
    /// The k3s pin the release installs; absent without release.json.
    public let k3s: String?

    public init(
        version: String, prerelease: Bool? = nil, publishedAt: Date? = nil, url: String? = nil,
        k3s: String? = nil
    ) {
        self.version = version
        self.prerelease = prerelease
        self.publishedAt = publishedAt
        self.url = url
        self.k3s = k3s
    }
}

/// A cluster node as `GET /v1/nodes` reports it (only the fields the app reads).
public struct ClusterNode: Codable, Hashable, Sendable, Identifiable {
    public let name: String
    public let role: String?
    public let ready: Bool

    public init(name: String, role: String? = nil, ready: Bool) {
        self.name = name
        self.role = role
        self.ready = ready
    }

    public var id: String { name }
}

struct NodesResponse: Decodable {
    let nodes: [ClusterNode]
}

/// A database pool (`GET /v1/system/database-pools`, the fields the app reads).
public struct DatabasePool: Codable, Hashable, Sendable, Identifiable {
    public let name: String
    public let engine: String?
    public let major: Int?
    public let state: String?

    public init(name: String, engine: String? = nil, major: Int? = nil, state: String? = nil) {
        self.name = name
        self.engine = engine
        self.major = major
        self.state = state
    }

    public var id: String { name }
}

struct DatabasePoolsResponse: Decodable {
    let pools: [DatabasePool]
}

/// The external S3 backup location (`BackupTarget`, never with its secret).
public struct BackupTarget: Codable, Hashable, Sendable {
    public let name: String
    public let endpoint: String
    public let bucket: String

    public init(name: String, endpoint: String, bucket: String) {
        self.name = name
        self.endpoint = endpoint
        self.bucket = bucket
    }
}

struct BackupTargetResponse: Decodable {
    let target: BackupTarget
}
