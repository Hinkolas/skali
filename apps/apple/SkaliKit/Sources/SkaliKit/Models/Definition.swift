import Foundation

/// A project's current draft (`GET /v1/projects/{id}/draft`), reduced to
/// what the app reads. The compiled definition keeps the manifest's
/// camelCase keys, which the snake_case decoding leaves alone.
public struct ProjectDraft: Codable, Hashable, Sendable {
    public let version: Int64
    public let hash: String
    public let definition: ProjectDefinition

    public init(version: Int64, hash: String, definition: ProjectDefinition) {
        self.version = version
        self.hash = hash
        self.definition = definition
    }
}

/// The compiled, target-independent definition: the project's services.
public struct ProjectDefinition: Codable, Hashable, Sendable {
    public let applications: [String: ApplicationSpec]?
    public let databases: [String: DatabaseSpec]?
    public let buckets: [String: BucketSpec]?

    public init(
        applications: [String: ApplicationSpec]? = nil, databases: [String: DatabaseSpec]? = nil,
        buckets: [String: BucketSpec]? = nil
    ) {
        self.applications = applications
        self.databases = databases
        self.buckets = buckets
    }

    public var serviceCount: Int {
        (applications?.count ?? 0) + (databases?.count ?? 0) + (buckets?.count ?? 0)
    }

    /// Whether a snapshot would hold anything: a database, a bucket or an
    /// application volume. Mirrors the server's planComponents.
    public var hasStatefulServices: Bool {
        !(databases ?? [:]).isEmpty || !(buckets ?? [:]).isEmpty
            || (applications ?? [:]).values.contains { !($0.volumes ?? [:]).isEmpty }
    }
}

public struct ApplicationSpec: Codable, Hashable, Sendable {
    public struct Source: Codable, Hashable, Sendable {
        /// `image` or `build`.
        public let kind: String
        public let image: String?

        public init(kind: String, image: String? = nil) {
            self.kind = kind
            self.image = image
        }
    }

    public struct Scaling: Codable, Hashable, Sendable {
        public let minReplicas: Int
        public let maxReplicas: Int

        public init(minReplicas: Int, maxReplicas: Int) {
            self.minReplicas = minReplicas
            self.maxReplicas = maxReplicas
        }
    }

    public struct Resources: Codable, Hashable, Sendable {
        public let limits: Values?

        public init(limits: Values?) {
            self.limits = limits
        }
    }

    public struct Values: Codable, Hashable, Sendable {
        public let milliCpu: Int?
        public let memoryBytes: Int64?

        public init(milliCpu: Int? = nil, memoryBytes: Int64? = nil) {
            self.milliCpu = milliCpu
            self.memoryBytes = memoryBytes
        }
    }

    public struct Volume: Codable, Hashable, Sendable {
        public let mountPath: String
        public let sizeBytes: Int64
    }

    public struct Route: Codable, Hashable, Sendable {
        public let path: String?
    }

    public let source: Source
    public let scaling: Scaling
    public let resources: Resources?
    public let routes: [String: Route]?
    public let volumes: [String: Volume]?

    public init(
        source: Source, scaling: Scaling, resources: Resources? = nil, routes: [String: Route]? = nil,
        volumes: [String: Volume]? = nil
    ) {
        self.source = source
        self.scaling = scaling
        self.resources = resources
        self.routes = routes
        self.volumes = volumes
    }
}

public struct DatabaseSpec: Codable, Hashable, Sendable {
    public let engine: String
    public let version: String
    public let isolation: String
    public let availability: String
    public let storageBytes: Int64?

    public init(engine: String, version: String, isolation: String = "shared", availability: String = "standard", storageBytes: Int64? = nil) {
        self.engine = engine
        self.version = version
        self.isolation = isolation
        self.availability = availability
        self.storageBytes = storageBytes
    }
}

public struct BucketSpec: Codable, Hashable, Sendable {
    /// `private` or `public`.
    public let visibility: String
    public let versioning: String
    public let storageQuotaBytes: Int64?

    public init(visibility: String, versioning: String = "disabled", storageQuotaBytes: Int64? = nil) {
        self.visibility = visibility
        self.versioning = versioning
        self.storageQuotaBytes = storageQuotaBytes
    }
}

struct DraftResponse: Decodable {
    let draft: ProjectDraft
}
