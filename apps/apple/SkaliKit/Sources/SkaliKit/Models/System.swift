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
    public let url: String?

    public init(version: String, prerelease: Bool? = nil, publishedAt: Date? = nil, url: String? = nil) {
        self.version = version
        self.prerelease = prerelease
        self.publishedAt = publishedAt
        self.url = url
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

/// The update channel a cluster follows.
public enum ReleaseChannel: String, Codable, Hashable, Sendable, CaseIterable, Identifiable {
    case stable, beta

    public var id: String { rawValue }
}

/// `GET /v1/system/updates` (`UpdateStatus`, the fields the app reads).
public struct UpdateStatus: Codable, Hashable, Sendable {
    public struct Installed: Codable, Hashable, Sendable {
        public let version: String

        public init(version: String) {
            self.version = version
        }
    }

    public struct Summary: Codable, Hashable, Sendable {
        /// `unknown`, `not_checked`, `no_release`, `current`, `available`,
        /// `incomplete`, `updating` or `failed`.
        public let state: String

        public init(state: String) {
            self.state = state
        }
    }

    public let installed: Installed
    public let summary: Summary
    public let channel: ReleaseChannel
    public let autoUpdate: Bool
    public let lastCheckedAt: Date?
    public let latest: Release?
    public let updateAvailable: Bool

    public init(
        installed: Installed, summary: Summary, channel: ReleaseChannel, autoUpdate: Bool,
        lastCheckedAt: Date? = nil, latest: Release? = nil, updateAvailable: Bool = false
    ) {
        self.installed = installed
        self.summary = summary
        self.channel = channel
        self.autoUpdate = autoUpdate
        self.lastCheckedAt = lastCheckedAt
        self.latest = latest
        self.updateAvailable = updateAvailable
    }
}

/// `PUT /v1/system/updates/settings`.
public struct UpdateSettings: Codable, Hashable, Sendable {
    public var channel: ReleaseChannel
    public var autoUpdate: Bool

    public init(channel: ReleaseChannel, autoUpdate: Bool) {
        self.channel = channel
        self.autoUpdate = autoUpdate
    }
}
