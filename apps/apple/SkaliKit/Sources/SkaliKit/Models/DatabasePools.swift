import Foundation

/// One managed PostgreSQL pool (`DatabasePool`), without its tuning
/// parameters, which the app does not show yet.
public struct DatabasePool: Codable, Hashable, Sendable, Identifiable {
    public struct Memory: Codable, Hashable, Sendable {
        /// The budget every parameter derives from; nil before a database
        /// node was observed.
        public let bytes: Int64?
        /// Sized automatically from the node in `node`.
        public let auto: Bool
        public let node: String?
        /// The automatic share was reduced so the live pools fit the node.
        public let capped: Bool?

        public init(bytes: Int64?, auto: Bool, node: String? = nil, capped: Bool? = nil) {
            self.bytes = bytes
            self.auto = auto
            self.node = node
            self.capped = capped
        }
    }

    public struct Observed: Codable, Hashable, Sendable {
        /// The operator's phase, e.g. "Cluster in healthy state".
        public let phase: String
        public let instances: Int
        public let readyInstances: Int
        public let primary: String?

        public init(phase: String, instances: Int, readyInstances: Int, primary: String? = nil) {
            self.phase = phase
            self.instances = instances
            self.readyInstances = readyInstances
            self.primary = primary
        }
    }

    /// Whether a new ordinary login succeeds (`DatabasePoolConnections`).
    public struct Connections: Codable, Hashable, Sendable {
        public enum State: String, Codable, Hashable, Sendable {
            case available, saturated, refusing, unknown

            public init(from decoder: any Decoder) throws {
                let raw = try decoder.singleValueContainer().decode(String.self)
                self = State(rawValue: raw) ?? .unknown
            }
        }

        public let state: State
        public let reason: String?
        public let since: Date?
        public let checkedAt: Date?
        public let used: Int?
        /// What ordinary roles may reach: max_connections less the reserved slots.
        public let limit: Int?
        public let maxConnections: Int?
        public let incident: Incident?

        public init(
            state: State, reason: String? = nil, since: Date? = nil, checkedAt: Date? = nil,
            used: Int? = nil, limit: Int? = nil, maxConnections: Int? = nil, incident: Incident? = nil
        ) {
            self.state = state
            self.reason = reason
            self.since = since
            self.checkedAt = checkedAt
            self.used = used
            self.limit = limit
            self.maxConnections = maxConnections
            self.incident = incident
        }
    }

    /// A stretch of time the pool refused new connections.
    public struct Incident: Codable, Hashable, Sendable, Identifiable {
        public let id: String
        public let code: String
        public let message: String
        public let openedAt: Date
        public let closedAt: Date?

        public init(id: String, code: String, message: String, openedAt: Date, closedAt: Date?) {
            self.id = id
            self.code = code
            self.message = message
            self.openedAt = openedAt
            self.closedAt = closedAt
        }
    }

    public let name: String
    /// `shared`, `environment` or `dedicated`.
    public let `class`: String
    public let engine: String
    public let major: Int
    public let instances: Int
    /// The volume per instance.
    public let storageBytes: Int64
    public let image: String
    public let nodePort: Int?
    public let state: String
    public let memory: Memory
    public let observed: Observed?
    public let connections: Connections?
    public let createdAt: Date
    public let updatedAt: Date

    public init(
        name: String, class: String, engine: String = "postgres", major: Int, instances: Int,
        storageBytes: Int64, image: String = "", nodePort: Int? = nil, state: String = "ready",
        memory: Memory, observed: Observed?, connections: Connections?, createdAt: Date, updatedAt: Date
    ) {
        self.name = name
        self.class = `class`
        self.engine = engine
        self.major = major
        self.instances = instances
        self.storageBytes = storageBytes
        self.image = image
        self.nodePort = nodePort
        self.state = state
        self.memory = memory
        self.observed = observed
        self.connections = connections
        self.createdAt = createdAt
        self.updatedAt = updatedAt
    }

    public var id: String { name }
}

/// `GET /v1/system/database-pools/{name}` (`DatabasePoolDetail`): a pool
/// with its instance pods, every database on it, and its newest incidents.
public struct DatabasePoolDetail: Decodable, Hashable, Sendable {
    /// One instance pod.
    public struct Member: Codable, Hashable, Sendable, Identifiable {
        public let name: String
        /// `primary` or `replica`; empty while the operator has not labeled it.
        public let role: String
        public let node: String?
        public let ready: Bool
        public let restarts: Int
        public let startedAt: Date?
        public let phase: String

        public init(
            name: String, role: String, node: String?, ready: Bool, restarts: Int,
            startedAt: Date?, phase: String
        ) {
            self.name = name
            self.role = role
            self.node = node
            self.ready = ready
            self.restarts = restarts
            self.startedAt = startedAt
            self.phase = phase
        }

        public var id: String { name }
    }

    /// One logical database on the pool.
    public struct Database: Codable, Hashable, Sendable, Identifiable {
        public struct ProjectRef: Codable, Hashable, Sendable {
            public let id: String
            public let name: String
            public let displayName: String

            public init(id: String, name: String, displayName: String = "") {
                self.id = id
                self.name = name
                self.displayName = displayName
            }

            public var label: String { displayName.isEmpty ? name : displayName }
        }

        public struct EnvironmentRef: Codable, Hashable, Sendable {
            public let id: String
            public let name: String

            public init(id: String, name: String) {
                self.id = id
                self.name = name
            }
        }

        public let databaseName: String
        public let roleName: String
        /// `service` for a project's declared database, `system` for the platform's.
        public let owner: String
        public let systemKey: String?
        public let serviceKey: String?
        public let project: ProjectRef?
        public let environment: EnvironmentRef?
        public let phase: String
        public let storageBytes: Int64
        /// The newest measured logical size; nil before the sampler saw it.
        public let usedBytes: Int64?
        /// Client connections at the newest check; nil before one succeeded.
        public let connections: Int?
        public let createdAt: Date

        public init(
            databaseName: String, roleName: String, owner: String, systemKey: String? = nil,
            serviceKey: String? = nil, project: ProjectRef? = nil, environment: EnvironmentRef? = nil,
            phase: String = "ready", storageBytes: Int64, usedBytes: Int64?, connections: Int?,
            createdAt: Date
        ) {
            self.databaseName = databaseName
            self.roleName = roleName
            self.owner = owner
            self.systemKey = systemKey
            self.serviceKey = serviceKey
            self.project = project
            self.environment = environment
            self.phase = phase
            self.storageBytes = storageBytes
            self.usedBytes = usedBytes
            self.connections = connections
            self.createdAt = createdAt
        }

        public var id: String { databaseName }

        public var isSystem: Bool { owner == "system" }

        /// "birefnet-proxy · production · db", or "system · registry".
        public var ownerLabel: String {
            if isSystem { return ["system", systemKey].compactMap(\.self).joined(separator: " · ") }
            return [project?.label ?? "unknown project", environment?.name, serviceKey]
                .compactMap(\.self).joined(separator: " · ")
        }
    }

    public let pool: DatabasePool
    public let members: [Member]
    public let databases: [Database]
    public let incidents: [DatabasePool.Incident]

    public init(pool: DatabasePool, members: [Member], databases: [Database], incidents: [DatabasePool.Incident]) {
        self.pool = pool
        self.members = members
        self.databases = databases
        self.incidents = incidents
    }

    private enum CodingKeys: String, CodingKey {
        case members, databases, incidents
    }

    public init(from decoder: any Decoder) throws {
        // allOf: the pool's fields and the detail's sit side by side.
        pool = try DatabasePool(from: decoder)
        let container = try decoder.container(keyedBy: CodingKeys.self)
        members = try container.decodeIfPresent([Member].self, forKey: .members) ?? []
        databases = try container.decodeIfPresent([Database].self, forKey: .databases) ?? []
        incidents = try container.decodeIfPresent([DatabasePool.Incident].self, forKey: .incidents) ?? []
    }

    /// Largest measured first, unmeasured after, by name.
    public var databasesBySize: [Database] {
        databases.sorted { a, b in
            let sizeA = a.usedBytes ?? -1, sizeB = b.usedBytes ?? -1
            return sizeA != sizeB ? sizeA > sizeB : a.databaseName < b.databaseName
        }
    }

    /// "1 primary · 1 replica", "no instances observed".
    public var memberSummary: String {
        if members.isEmpty { return "no instances observed" }
        let primaries = members.count { $0.role == "primary" }
        let replicas = members.count { $0.role == "replica" }
        var parts: [String] = []
        if primaries > 0 { parts.append(primaries == 1 ? "1 primary" : "\(primaries) primaries") }
        if replicas > 0 { parts.append(replicas == 1 ? "1 replica" : "\(replicas) replicas") }
        let starting = members.count - primaries - replicas
        if starting > 0 { parts.append("\(starting) starting") }
        return parts.joined(separator: " · ")
    }
}

/// `GET /v1/system/database-pools/{name}/metrics` (`DatabasePoolMetrics`).
/// CPU and memory sum the instances; connections, transactions, cache and
/// database size are the primary's exporter view.
public struct DatabasePoolMetrics: Codable, Hashable, Sendable {
    /// The newest sample within the last minutes, with its denominators.
    public struct Current: Codable, Hashable, Sendable {
        public let sampledAt: Date
        public let cpuMillicores: Int
        public let memoryBytes: Int64
        public let instances: Int
        public let instancesReady: Int
        public let connections: Int?
        public let databaseBytes: Int64?
        public let memoryBudgetBytes: Int64?
        public let maxConnections: Int?

        public init(
            sampledAt: Date, cpuMillicores: Int, memoryBytes: Int64, instances: Int, instancesReady: Int,
            connections: Int?, databaseBytes: Int64?, memoryBudgetBytes: Int64?, maxConnections: Int?
        ) {
            self.sampledAt = sampledAt
            self.cpuMillicores = cpuMillicores
            self.memoryBytes = memoryBytes
            self.instances = instances
            self.instancesReady = instancesReady
            self.connections = connections
            self.databaseBytes = databaseBytes
            self.memoryBudgetBytes = memoryBudgetBytes
            self.maxConnections = maxConnections
        }
    }

    public let pool: String
    public let window: NodeMetrics.Window
    public let stepSeconds: Int
    public let timestamps: [Date]
    public let cpuMillicores: [Int?]
    public let memoryBytes: [Int64?]
    public let connections: [Int?]
    public let commits: [Int?]
    public let rollbacks: [Int?]
    public let blksHit: [Int64?]
    public let blksRead: [Int64?]
    public let cacheHitRatio: [Double?]
    public let databaseBytes: [Int64?]
    public let instancesReady: [Int?]
    public let current: Current?

    public init(
        pool: String, window: NodeMetrics.Window, stepSeconds: Int, timestamps: [Date],
        cpuMillicores: [Int?], memoryBytes: [Int64?], connections: [Int?], commits: [Int?],
        rollbacks: [Int?], blksHit: [Int64?], blksRead: [Int64?], cacheHitRatio: [Double?],
        databaseBytes: [Int64?], instancesReady: [Int?], current: Current?
    ) {
        self.pool = pool
        self.window = window
        self.stepSeconds = stepSeconds
        self.timestamps = timestamps
        self.cpuMillicores = cpuMillicores
        self.memoryBytes = memoryBytes
        self.connections = connections
        self.commits = commits
        self.rollbacks = rollbacks
        self.blksHit = blksHit
        self.blksRead = blksRead
        self.cacheHitRatio = cacheHitRatio
        self.databaseBytes = databaseBytes
        self.instancesReady = instancesReady
        self.current = current
    }

    /// Per-bucket transactions: commits plus rollbacks.
    public var transactions: [Int?] {
        zip(commits, rollbacks).map { commits, rollbacks in
            commits == nil && rollbacks == nil ? nil : (commits ?? 0) + (rollbacks ?? 0)
        }
    }

    /// Whether any series has a sample to draw.
    public var hasSamples: Bool {
        cpuMillicores.contains { $0 != nil } || connections.contains { $0 != nil }
    }
}
