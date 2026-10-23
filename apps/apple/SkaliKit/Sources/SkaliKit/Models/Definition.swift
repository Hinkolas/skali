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
    /// What each application uses, by dotted name: `applications.web` to
    /// `["databases.main", "buckets.media"]`.
    public let dependencies: [String: [String]]?

    public init(
        applications: [String: ApplicationSpec]? = nil, databases: [String: DatabaseSpec]? = nil,
        buckets: [String: BucketSpec]? = nil, dependencies: [String: [String]]? = nil
    ) {
        self.applications = applications
        self.databases = databases
        self.buckets = buckets
        self.dependencies = dependencies
    }

    public var serviceCount: Int {
        (applications?.count ?? 0) + (databases?.count ?? 0) + (buckets?.count ?? 0)
    }

    /// The databases and buckets an application uses, as `(kind, key)`.
    public func dependencies(of application: String) -> [(kind: ServiceKind, key: String)] {
        (dependencies?["applications.\(application)"] ?? []).compactMap { reference in
            let parts = reference.split(separator: ".", maxSplits: 1).map(String.init)
            guard parts.count == 2 else { return nil }
            switch parts[0] {
            case "databases": return (.database, parts[1])
            case "buckets": return (.bucket, parts[1])
            default: return nil
            }
        }
    }

    /// The applications that use a database or bucket, by key.
    public func dependents(of kind: ServiceKind, _ key: String) -> [String] {
        let reference = "\(kind == .database ? "databases" : "buckets").\(key)"
        return (dependencies ?? [:]).filter { $0.value.contains(reference) }
            .compactMap { $0.key.hasPrefix("applications.") ? String($0.key.dropFirst("applications.".count)) : nil }
            .sorted()
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

        public init(mountPath: String, sizeBytes: Int64) {
            self.mountPath = mountPath
            self.sizeBytes = sizeBytes
        }
    }

    public struct Route: Codable, Hashable, Sendable {
        public let path: String?

        public init(path: String?) {
            self.path = path
        }
    }

    public struct Port: Codable, Hashable, Sendable {
        public let port: Int
        public let `protocol`: String

        public init(port: Int, protocol: String = "http") {
            self.port = port
            self.protocol = `protocol`
        }
    }

    public struct Health: Codable, Hashable, Sendable {
        public struct Probe: Codable, Hashable, Sendable {
            public struct HTTP: Codable, Hashable, Sendable {
                public let path: String
            }

            public let http: HTTP?
        }

        public let readiness: Probe?
        public let liveness: Probe?

        public init(readinessPath: String?) {
            readiness = readinessPath.map { Probe(http: Probe.HTTP(path: $0)) }
            liveness = nil
        }
    }

    public struct Deployment: Codable, Hashable, Sendable {
        public struct Rollout: Codable, Hashable, Sendable {
            public let strategy: String?
        }

        public let rollout: Rollout?

        public init(strategy: String?) {
            rollout = Rollout(strategy: strategy)
        }
    }

    public let source: Source
    public let command: [String]?
    public let ports: [String: Port]?
    public let health: Health?
    public let scaling: Scaling
    public let resources: Resources?
    public let routes: [String: Route]?
    public let deployment: Deployment?
    public let volumes: [String: Volume]?

    public init(
        source: Source, command: [String]? = nil, ports: [String: Port]? = nil, health: Health? = nil,
        scaling: Scaling, resources: Resources? = nil, routes: [String: Route]? = nil,
        deployment: Deployment? = nil, volumes: [String: Volume]? = nil
    ) {
        self.source = source
        self.command = command
        self.ports = ports
        self.health = health
        self.scaling = scaling
        self.resources = resources
        self.routes = routes
        self.deployment = deployment
        self.volumes = volumes
    }

    /// "ghcr.io/eplan/b1:2.4.0", "build".
    public var sourceLabel: String {
        source.kind == "image" ? (source.image ?? "image") : "build"
    }

    /// "web 8080/http · metrics 9090/http", nil without ports.
    public var portsLabel: String? {
        guard let ports, !ports.isEmpty else { return nil }
        return ports.sorted { $0.key < $1.key }.map { "\($0.key) \($0.value.port)/\($0.value.protocol)" }
            .joined(separator: " · ")
    }

    /// "http /healthz", nil without an HTTP probe.
    public var healthCheckLabel: String? {
        (health?.readiness?.http ?? health?.liveness?.http).map { "http \($0.path)" }
    }

    /// "blue-green" unless the manifest picks another strategy.
    public var rolloutStrategy: String {
        deployment?.rollout?.strategy ?? "blue-green"
    }

    /// "2", "1–4".
    public var scaleLabel: String {
        scaling.minReplicas == scaling.maxReplicas ? "\(scaling.minReplicas)" : "\(scaling.minReplicas)–\(scaling.maxReplicas)"
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
