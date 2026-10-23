import Foundation

/// An environment's topology and health projection
/// (`GET /v1/environments/{id}/status`, and every event of its stream).
public struct EnvironmentStatus: Codable, Hashable, Sendable {
    public let environmentId: String
    /// `active`, `down` or `releasing`.
    public let state: String
    public let targetRevision: RevisionRef?
    public let activeRevision: RevisionRef?
    public let observation: StatusObservation
    public let services: [ServiceStatus]
    /// The restore holding the environment down, nil when none does.
    public let incompleteRestore: IncompleteRestore?

    public init(
        environmentId: String, state: String = "active", targetRevision: RevisionRef? = nil,
        activeRevision: RevisionRef? = nil, observation: StatusObservation = StatusObservation(state: "fresh"),
        services: [ServiceStatus], incompleteRestore: IncompleteRestore? = nil
    ) {
        self.environmentId = environmentId
        self.state = state
        self.targetRevision = targetRevision
        self.activeRevision = activeRevision
        self.observation = observation
        self.services = services
        self.incompleteRestore = incompleteRestore
    }

    /// The live status of one service of the definition.
    public func service(_ kind: ServiceKind, _ key: String) -> ServiceStatus? {
        services.first { $0.type == kind.rawValue && $0.key == key }
    }
}

public struct RevisionRef: Codable, Hashable, Sendable {
    public let id: String
    public let checksum: String

    public init(id: String, checksum: String) {
        self.id = id
        self.checksum = checksum
    }
}

/// How fresh the observed state behind a projection is; anything but
/// `fresh` reports health as unknown.
public struct StatusObservation: Codable, Hashable, Sendable {
    /// `fresh`, `stale` or `unknown`.
    public let state: String
    public let staleSince: Date?
    public let lastSync: Date?

    public init(state: String, staleSince: Date? = nil, lastSync: Date? = nil) {
        self.state = state
        self.staleSince = staleSince
        self.lastSync = lastSync
    }
}

public struct ServiceStatus: Codable, Hashable, Sendable {
    public let key: String
    /// `application`, `database` or `bucket`.
    public let type: String
    public let health: ServiceHealth
    public let diagnostics: [HealthDiagnostic]
    public let pods: [PodStatus]
    public let routes: [RouteStatus]?

    public init(
        key: String, type: String, health: ServiceHealth, diagnostics: [HealthDiagnostic] = [],
        pods: [PodStatus] = [], routes: [RouteStatus]? = nil
    ) {
        self.key = key
        self.type = type
        self.health = health
        self.diagnostics = diagnostics
        self.pods = pods
        self.routes = routes
    }

    /// Routes, aliases included, whose TLS waits for the domain to point here.
    public var deferredRoutes: Int {
        (routes ?? []).reduce(0) { count, route in
            count + (route.edge?.deferred == true ? 1 : 0)
                + (route.aliases ?? []).filter { $0.edge?.deferred == true }.count
        }
    }
}

public struct HealthDiagnostic: Codable, Hashable, Sendable {
    /// `info`, `warning` or `error`.
    public let severity: String
    public let code: String
    public let message: String
    public let resource: String?

    public init(severity: String, code: String, message: String, resource: String? = nil) {
        self.severity = severity
        self.code = code
        self.message = message
        self.resource = resource
    }
}

public struct PodStatus: Codable, Hashable, Sendable {
    public let name: String
    public let node: String
    public let phase: String
    public let ready: Bool
    public let restarts: Int
    public let reason: String?
    public let startedAt: Date?

    public init(
        name: String, node: String, phase: String = "Running", ready: Bool = true, restarts: Int = 0,
        reason: String? = nil, startedAt: Date? = nil
    ) {
        self.name = name
        self.node = node
        self.phase = phase
        self.ready = ready
        self.restarts = restarts
        self.reason = reason
        self.startedAt = startedAt
    }
}

/// A public route, reduced to what the overview reads.
public struct RouteStatus: Codable, Hashable, Sendable {
    public let key: String
    public let domain: String
    public let edge: EdgeStatus?
    public let aliases: [RouteAliasStatus]?

    public init(key: String, domain: String, edge: EdgeStatus? = nil, aliases: [RouteAliasStatus]? = nil) {
        self.key = key
        self.domain = domain
        self.edge = edge
        self.aliases = aliases
    }
}

public struct RouteAliasStatus: Codable, Hashable, Sendable {
    public let domain: String
    public let edge: EdgeStatus?
}

/// The reconciler's last probe of whether a domain reaches the cluster.
public struct EdgeStatus: Codable, Hashable, Sendable {
    public let state: String
    /// TLS waits for the domain to point here.
    public let deferred: Bool

    public init(state: String, deferred: Bool) {
        self.state = state
        self.deferred = deferred
    }
}

/// A restore that stopped its environment and did not complete.
public struct IncompleteRestore: Codable, Hashable, Sendable {
    public let id: String
    public let snapshotId: String
    /// `running`, `failed` or `cancelled`.
    public let status: String
    public let error: String?
    public let startedAt: Date
}
