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

/// A cluster node as `GET /v1/nodes` observes it (`Node`).
public struct ClusterNode: Codable, Hashable, Sendable, Identifiable {
    public let name: String
    /// `server` (a controller) or `agent` (a worker).
    public let role: String?
    /// Extra duties: `edge`, `builder`, `database`.
    public let capabilities: [String]
    public let arch: String?
    public let os: String?
    public let kubeletVersion: String?
    public let ready: Bool
    /// False once the node is cordoned.
    public let schedulable: Bool
    public let internalIp: String?
    public let externalIp: String?
    /// The Ready condition's heartbeat, nil until observed.
    public let lastHeartbeat: Date?
    public let memoryAllocatableBytes: Int64?

    public init(
        name: String, role: String? = nil, capabilities: [String] = [], arch: String? = nil,
        os: String? = nil, kubeletVersion: String? = nil, ready: Bool, schedulable: Bool = true,
        internalIp: String? = nil, externalIp: String? = nil, lastHeartbeat: Date? = nil,
        memoryAllocatableBytes: Int64? = nil
    ) {
        self.name = name
        self.role = role
        self.capabilities = capabilities
        self.arch = arch
        self.os = os
        self.kubeletVersion = kubeletVersion
        self.ready = ready
        self.schedulable = schedulable
        self.internalIp = internalIp
        self.externalIp = externalIp
        self.lastHeartbeat = lastHeartbeat
        self.memoryAllocatableBytes = memoryAllocatableBytes
    }

    public init(from decoder: any Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        name = try container.decode(String.self, forKey: .name)
        role = try container.decodeIfPresent(String.self, forKey: .role)
        capabilities = try container.decodeIfPresent([String].self, forKey: .capabilities) ?? []
        arch = try container.decodeIfPresent(String.self, forKey: .arch)
        os = try container.decodeIfPresent(String.self, forKey: .os)
        kubeletVersion = try container.decodeIfPresent(String.self, forKey: .kubeletVersion)
        ready = try container.decode(Bool.self, forKey: .ready)
        schedulable = try container.decodeIfPresent(Bool.self, forKey: .schedulable) ?? true
        internalIp = try container.decodeIfPresent(String.self, forKey: .internalIp)
        externalIp = try container.decodeIfPresent(String.self, forKey: .externalIp)
        lastHeartbeat = try container.decodeIfPresent(Date.self, forKey: .lastHeartbeat)
        memoryAllocatableBytes = try container.decodeIfPresent(Int64.self, forKey: .memoryAllocatableBytes)
    }

    public var id: String { name }

    public var isController: Bool { role == "server" }

    /// "linux/arm64", or whichever half is known.
    public var platform: String? {
        switch (os, arch) {
        case let (os?, arch?): "\(os)/\(arch)"
        case let (os?, nil): os
        case let (nil, arch?): arch
        case (nil, nil): nil
        }
    }
}

/// `GET /v1/nodes/metrics` (`NodeMetrics`): CPU and memory per node,
/// bucketed onto a fixed step grid; nil marks a bucket without samples.
public struct NodeMetrics: Codable, Hashable, Sendable {
    public enum Window: String, Codable, CaseIterable, Identifiable, Sendable {
        case hour = "1h", day = "24h", week = "7d"

        public var id: String { rawValue }
    }

    public struct Series: Codable, Hashable, Sendable {
        public let name: String
        public let cpuAllocatableMillicores: Int
        public let memoryAllocatableBytes: Int64
        public let cpuMillicores: [Int?]
        public let memoryBytes: [Int64?]

        public init(
            name: String, cpuAllocatableMillicores: Int, memoryAllocatableBytes: Int64,
            cpuMillicores: [Int?], memoryBytes: [Int64?]
        ) {
            self.name = name
            self.cpuAllocatableMillicores = cpuAllocatableMillicores
            self.memoryAllocatableBytes = memoryAllocatableBytes
            self.cpuMillicores = cpuMillicores
            self.memoryBytes = memoryBytes
        }
    }

    public let window: Window
    public let stepSeconds: Int
    public let timestamps: [Date]
    public let nodes: [Series]

    public init(window: Window, stepSeconds: Int, timestamps: [Date], nodes: [Series]) {
        self.window = window
        self.stepSeconds = stepSeconds
        self.timestamps = timestamps
        self.nodes = nodes
    }
}

/// `GET /v1/nodes/storage` (`NodesStorage`): the newest disk sample per node.
public struct NodeStorage: Codable, Hashable, Sendable {
    public let name: String
    public let sampledAt: Date
    public let capacityBytes: Int64
    public let usedBytes: Int64
    public let availableBytes: Int64

    public init(name: String, sampledAt: Date, capacityBytes: Int64, usedBytes: Int64, availableBytes: Int64) {
        self.name = name
        self.sampledAt = sampledAt
        self.capacityBytes = capacityBytes
        self.usedBytes = usedBytes
        self.availableBytes = availableBytes
    }
}

struct NodesStorageResponse: Decodable {
    let nodes: [NodeStorage]
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
