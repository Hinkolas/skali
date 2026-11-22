import Foundation

/// The installation's SeaweedFS object store
/// (`GET /v1/system/object-storage`): its shape, the volume size limit
/// that decides how many buckets fit, and the volume servers' slots.
public struct ObjectStore: Codable, Hashable, Sendable {
    public struct VolumeSize: Codable, Hashable, Sendable {
        public let limitMb: Int
        /// The limit is the platform default; no admin set one.
        public let `default`: Bool
        public let defaultMb: Int
        public let minMb: Int
        public let maxMb: Int

        public init(limitMb: Int, default: Bool, defaultMb: Int, minMb: Int, maxMb: Int) {
            self.limitMb = limitMb
            self.default = `default`
            self.defaultMb = defaultMb
            self.minMb = minMb
            self.maxMb = maxMb
        }
    }

    /// One volume server's slots, with the inputs of an estimate under
    /// another limit.
    public struct Server: Codable, Hashable, Sendable, Identifiable {
        public let url: String
        public let node: String?
        public let slots: Int
        public let usedSlots: Int
        public let freeSlots: Int
        /// The node's newest sampled free disk; nil when unknown.
        public let freeDiskBytes: Int64?
        /// Sizes of the server's writable volumes; nil when unread.
        public let writableVolumeBytes: [Int64]?

        public var id: String { url }

        public init(
            url: String, node: String? = nil, slots: Int, usedSlots: Int, freeSlots: Int,
            freeDiskBytes: Int64? = nil, writableVolumeBytes: [Int64]? = nil
        ) {
            self.url = url
            self.node = node
            self.slots = slots
            self.usedSlots = usedSlots
            self.freeSlots = freeSlots
            self.freeDiskBytes = freeDiskBytes
            self.writableVolumeBytes = writableVolumeBytes
        }
    }

    /// The volume servers' slot accounting, read from the master on request.
    public struct Capacity: Codable, Hashable, Sendable {
        public let slots: Int
        public let usedSlots: Int
        public let freeSlots: Int
        /// More volumes that can grow with every copy on a different server.
        public let volumeHeadroom: Int
        /// More buckets that fit before a new bucket's first write finds no room.
        public let bucketHeadroom: Int
        public let volumesPerBucket: Int
        public let copies: Int
        public let servers: [Server]

        public init(
            slots: Int, usedSlots: Int, freeSlots: Int, volumeHeadroom: Int, bucketHeadroom: Int,
            volumesPerBucket: Int, copies: Int, servers: [Server]
        ) {
            self.slots = slots
            self.usedSlots = usedSlots
            self.freeSlots = freeSlots
            self.volumeHeadroom = volumeHeadroom
            self.bucketHeadroom = bucketHeadroom
            self.volumesPerBucket = volumesPerBucket
            self.copies = copies
            self.servers = servers
        }
    }

    /// The provider observer's newest pass.
    public struct Observed: Codable, Hashable, Sendable {
        /// fresh, stale or unknown.
        public let state: String
        public let staleSince: Date?
        public let mastersReady: Int
        public let volumeServersReady: Int
        public let filerReady: Bool
        public let s3Ready: Bool
        public let s3Detail: String?
        public let underReplicatedVolumes: Int
        public let replicationPendingVolumes: Int
        public let volumeSizeLimitMb: Int

        public init(
            state: String, staleSince: Date? = nil, mastersReady: Int, volumeServersReady: Int,
            filerReady: Bool, s3Ready: Bool, s3Detail: String? = nil, underReplicatedVolumes: Int = 0,
            replicationPendingVolumes: Int = 0, volumeSizeLimitMb: Int
        ) {
            self.state = state
            self.staleSince = staleSince
            self.mastersReady = mastersReady
            self.volumeServersReady = volumeServersReady
            self.filerReady = filerReady
            self.s3Ready = s3Ready
            self.s3Detail = s3Detail
            self.underReplicatedVolumes = underReplicatedVolumes
            self.replicationPendingVolumes = replicationPendingVolumes
            self.volumeSizeLimitMb = volumeSizeLimitMb
        }
    }

    public let name: String
    public let state: String
    public let image: String
    /// False for the local platform's all-in-one process.
    public let managed: Bool
    public let masters: Int
    public let volumeServers: Int
    /// The SeaweedFS replication code, e.g. "001".
    public let replication: String
    public let volumeSize: VolumeSize
    /// Live buckets allocated on the store.
    public let buckets: Int
    /// Nil when the master did not answer.
    public let capacity: Capacity?
    public let observed: Observed?
    public let createdAt: Date
    public let updatedAt: Date

    public init(
        name: String = "seaweed", state: String = "active", image: String, managed: Bool, masters: Int,
        volumeServers: Int, replication: String, volumeSize: VolumeSize, buckets: Int, capacity: Capacity?,
        observed: Observed?, createdAt: Date, updatedAt: Date
    ) {
        self.name = name
        self.state = state
        self.image = image
        self.managed = managed
        self.masters = masters
        self.volumeServers = volumeServers
        self.replication = replication
        self.volumeSize = volumeSize
        self.buckets = buckets
        self.capacity = capacity
        self.observed = observed
        self.createdAt = createdAt
        self.updatedAt = updatedAt
    }
}

/// One collection's footprint on the volume servers, counted once
/// whatever the replication.
public struct ObjectStoreUsage: Codable, Hashable, Sendable {
    /// What the quota is judged on: deleted objects no longer count.
    public let usedBytes: Int64
    /// What the volumes hold until the vacuum reclaims deleted objects.
    public let diskBytes: Int64
    public let deletedBytes: Int64
    /// Live entries; a large object is several entries.
    public let entryCount: Int64
    public let volumes: Int
    /// Volume copies: the slots the collection holds.
    public let slots: Int

    public init(usedBytes: Int64, diskBytes: Int64, deletedBytes: Int64 = 0, entryCount: Int64, volumes: Int, slots: Int) {
        self.usedBytes = usedBytes
        self.diskBytes = diskBytes
        self.deletedBytes = deletedBytes
        self.entryCount = entryCount
        self.volumes = volumes
        self.slots = slots
    }
}

/// Every bucket on the store with its usage
/// (`GET /v1/system/object-storage/buckets`).
public struct ObjectStoreBuckets: Codable, Hashable, Sendable {
    public struct Bucket: Codable, Hashable, Sendable, Identifiable {
        public struct Project: Codable, Hashable, Sendable {
            public let id: String
            public let name: String
            public let displayName: String?

            public init(id: String, name: String, displayName: String? = nil) {
                self.id = id
                self.name = name
                self.displayName = displayName
            }
        }

        public struct Environment: Codable, Hashable, Sendable {
            public let id: String
            public let name: String

            public init(id: String, name: String) {
                self.id = id
                self.name = name
            }
        }

        public let bucketName: String
        /// service or system.
        public let owner: String
        public let systemKey: String?
        public let serviceKey: String?
        public let project: Project?
        public let environment: Environment?
        public let phase: String
        /// private or public-read.
        public let visibility: String
        /// 0 means no quota.
        public let quotaBytes: Int64
        /// Nil when the master did not answer.
        public let usage: ObjectStoreUsage?
        public let createdAt: Date

        public var id: String { bucketName }

        public init(
            bucketName: String, owner: String, systemKey: String? = nil, serviceKey: String? = nil,
            project: Project? = nil, environment: Environment? = nil, phase: String = "provisioned",
            visibility: String = "private", quotaBytes: Int64 = 0, usage: ObjectStoreUsage?, createdAt: Date
        ) {
            self.bucketName = bucketName
            self.owner = owner
            self.systemKey = systemKey
            self.serviceKey = serviceKey
            self.project = project
            self.environment = environment
            self.phase = phase
            self.visibility = visibility
            self.quotaBytes = quotaBytes
            self.usage = usage
            self.createdAt = createdAt
        }

        public var isSystem: Bool { owner == "system" }

        /// "Shop · production · uploads", "system · platform/registry".
        public var ownerLabel: String {
            if isSystem { return "system · \(systemKey ?? "")" }
            var parts: [String] = []
            if let project {
                parts.append(project.displayName.flatMap { $0.isEmpty ? nil : $0 } ?? project.name)
            }
            if let environment { parts.append(environment.name) }
            if let serviceKey, !serviceKey.isEmpty { parts.append(serviceKey) }
            return parts.isEmpty ? "unknown project" : parts.joined(separator: " · ")
        }
    }

    /// A collection no live bucket claims: left behind by a deletion that
    /// did not finish, or created outside Skali. It holds its slots.
    public struct Collection: Codable, Hashable, Sendable, Identifiable {
        public let collection: String
        public let usage: ObjectStoreUsage

        public var id: String { collection }

        public init(collection: String, usage: ObjectStoreUsage) {
            self.collection = collection
            self.usage = usage
        }
    }

    public let buckets: [Bucket]
    public let unclaimed: [Collection]
    /// False when the master did not answer: every usage is nil.
    public let usageObserved: Bool

    public init(buckets: [Bucket], unclaimed: [Collection], usageObserved: Bool) {
        self.buckets = buckets
        self.unclaimed = unclaimed
        self.usageObserved = usageObserved
    }

    /// Largest first by stored bytes, then by slots held; unread usage last.
    public var bucketsBySize: [Bucket] {
        buckets.sorted { a, b in
            let aUsed = a.usage?.usedBytes ?? -1, bUsed = b.usage?.usedBytes ?? -1
            if aUsed != bUsed { return aUsed > bUsed }
            let aSlots = a.usage?.slots ?? -1, bSlots = b.usage?.slots ?? -1
            if aSlots != bSlots { return aSlots > bSlots }
            return a.bucketName < b.bucketName
        }
    }

    /// Every bucket's stored bytes, leftovers included.
    public var storedBytes: Int64 {
        buckets.reduce(0) { $0 + ($1.usage?.usedBytes ?? 0) } + unclaimed.reduce(0) { $0 + $1.usage.usedBytes }
    }

    /// Bytes deleted but not yet reclaimed by the vacuum.
    public var awaitingVacuumBytes: Int64 {
        buckets.reduce(0) { $0 + ($1.usage?.deletedBytes ?? 0) } + unclaimed.reduce(0) { $0 + $1.usage.deletedBytes }
    }
}

/// `PUT /v1/system/object-storage/settings`: nil returns the volume size
/// limit to the default, so it is sent as an explicit null.
public struct ObjectStorageSettings: Encodable, Hashable, Sendable {
    public var volumeSizeLimitMb: Int?

    public init(volumeSizeLimitMb: Int?) {
        self.volumeSizeLimitMb = volumeSizeLimitMb
    }

    private enum CodingKeys: String, CodingKey {
        case volumeSizeLimitMb
    }

    public func encode(to encoder: any Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        if let volumeSizeLimitMb {
            try container.encode(volumeSizeLimitMb, forKey: .volumeSizeLimitMb)
        } else {
            try container.encodeNil(forKey: .volumeSizeLimitMb)
        }
    }
}

struct ObjectStoreResponse: Decodable {
    let store: ObjectStore
}
