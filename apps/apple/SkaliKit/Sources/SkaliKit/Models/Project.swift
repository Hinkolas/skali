import Foundation

/// A project as `GET /v1/projects?include=summary` lists it (`Project`).
public struct Project: Codable, Hashable, Sendable, Identifiable {
    public let id: String
    public let name: String
    public let displayName: String
    /// `managed` or `file`.
    public let sourceMode: String
    public let createdAt: Date
    /// When the project's own settings last changed; deploys never move it.
    public let updatedAt: Date
    public let deletingAt: Date?
    public let access: ProjectAccess
    public let summary: ProjectSummary?

    public init(
        id: String, name: String, displayName: String = "", sourceMode: String = "managed",
        createdAt: Date, updatedAt: Date, deletingAt: Date? = nil,
        access: ProjectAccess, summary: ProjectSummary?
    ) {
        self.id = id
        self.name = name
        self.displayName = displayName
        self.sourceMode = sourceMode
        self.createdAt = createdAt
        self.updatedAt = updatedAt
        self.deletingAt = deletingAt
        self.access = access
        self.summary = summary
    }

    /// The name listings show.
    public var label: String { displayName.isEmpty ? name : displayName }
}

/// The caller's standing on a project (`ProjectAccess`).
public struct ProjectAccess: Codable, Hashable, Sendable {
    public let role: String
    /// False only for an instance admin who sees the project without a membership.
    public let member: Bool
    public let environments: [String: String]

    public init(role: String, member: Bool, environments: [String: String] = [:]) {
        self.role = role
        self.member = member
        self.environments = environments
    }
}

/// The list rollup `?include=summary` attaches (`ProjectSummary`).
public struct ProjectSummary: Codable, Hashable, Sendable {
    public let environments: [SummaryEnvironment]
    public let serviceCounts: ServiceCounts
    /// The project's last activity.
    public let updatedAt: Date

    public init(environments: [SummaryEnvironment], serviceCounts: ServiceCounts, updatedAt: Date) {
        self.environments = environments
        self.serviceCounts = serviceCounts
        self.updatedAt = updatedAt
    }
}

public struct SummaryEnvironment: Codable, Hashable, Sendable, Identifiable {
    public let id: String
    public let name: String
    /// The caller's role here; `none` marks a locked environment that
    /// carries nothing beyond id, name and access.
    public let access: String
    /// `normal` or `high`.
    public let priority: String?
    /// `active`, `down` or `releasing`.
    public let state: String?
    public let health: ServiceHealth?
    public let healthEvaluatedAt: Date?

    public init(
        id: String, name: String, access: String = "admin", priority: String? = "normal",
        state: String? = "active", health: ServiceHealth? = .healthy, healthEvaluatedAt: Date? = nil
    ) {
        self.id = id
        self.name = name
        self.access = access
        self.priority = priority
        self.state = state
        self.health = health
        self.healthEvaluatedAt = healthEvaluatedAt
    }

    public var isHighPriority: Bool { priority == "high" }
}

public struct ServiceCounts: Codable, Hashable, Sendable {
    public let applications: Int
    public let databases: Int
    public let buckets: Int

    public init(applications: Int, databases: Int, buckets: Int) {
        self.applications = applications
        self.databases = databases
        self.buckets = buckets
    }

    public var total: Int { applications + databases + buckets }
}

/// Service health from the status projection. A value a newer daemon adds
/// decodes as `unknown` rather than failing the whole list.
public enum ServiceHealth: String, Codable, Hashable, Sendable, CaseIterable {
    case healthy, unknown, progressing, degraded, unhealthy

    public init(from decoder: any Decoder) throws {
        let raw = try decoder.singleValueContainer().decode(String.self)
        self = ServiceHealth(rawValue: raw) ?? .unknown
    }
}

/// The three service kinds the platform serves.
public enum ServiceKind: String, Hashable, Sendable, CaseIterable {
    case application, database, bucket
}

struct ProjectsResponse: Decodable {
    let projects: [Project]
}
