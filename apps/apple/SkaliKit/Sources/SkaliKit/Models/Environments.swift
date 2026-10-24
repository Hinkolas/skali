import Foundation

/// One environment of a project as `GET /v1/projects/{id}/environments`
/// lists it (`Environment`). A locked environment (`access` `none`)
/// carries id, project, name and access only.
public struct ProjectEnvironment: Codable, Hashable, Sendable, Identifiable {
    public let id: String
    public let projectId: String
    public let name: String
    public let previousNames: [String]?
    /// The caller's effective role here.
    public let access: String
    public let createdAt: Date?
    public let settings: EnvironmentSettings?
    /// The environment this one was last promoted to.
    public let lastPromotionTarget: String?
    /// `active`, `down` or `releasing`; with `?include=summary` only.
    public let state: String?
    /// The kernel's cached worst service health; with `?include=summary` only.
    public let health: ServiceHealth?
    public let healthEvaluatedAt: Date?

    public init(
        id: String, projectId: String, name: String, previousNames: [String]? = nil,
        access: String = "admin", createdAt: Date? = nil, settings: EnvironmentSettings? = nil,
        lastPromotionTarget: String? = nil, state: String? = "active", health: ServiceHealth? = .healthy,
        healthEvaluatedAt: Date? = nil
    ) {
        self.id = id
        self.projectId = projectId
        self.name = name
        self.previousNames = previousNames
        self.access = access
        self.createdAt = createdAt
        self.settings = settings
        self.lastPromotionTarget = lastPromotionTarget
        self.state = state
        self.health = health
        self.healthEvaluatedAt = healthEvaluatedAt
    }

    public var role: AccessRole { AccessRole(rawValue: access) ?? .none }
    public var isLocked: Bool { role == .none }
    public var isHighPriority: Bool { settings?.priority == "high" }

    /// Whether `name` is this environment's name or one it was renamed from.
    public func answers(to name: String) -> Bool {
        self.name == name || previousNames?.contains(name) == true
    }
}

/// Server-side environment settings (`EnvironmentSettings`).
public struct EnvironmentSettings: Codable, Hashable, Sendable {
    public let maxRole: String
    /// `direct` or `promote-only`.
    public let deployPolicy: String
    public let promoteFrom: [String]
    /// `normal` or `high`.
    public let priority: String
    /// The automatic backup schedule, nil when off.
    public let backup: BackupSchedule?

    public init(
        maxRole: String = "admin", deployPolicy: String = "direct", promoteFrom: [String] = [],
        priority: String = "normal", backup: BackupSchedule? = nil
    ) {
        self.maxRole = maxRole
        self.deployPolicy = deployPolicy
        self.promoteFrom = promoteFrom
        self.priority = priority
        self.backup = backup
    }
}

/// An environment's automatic backups (`BackupSchedule`).
public struct BackupSchedule: Codable, Hashable, Sendable {
    /// Five-field cron in UTC, such as `0 3 * * *`.
    public let schedule: String
    public let retentionSeconds: Int64

    public init(schedule: String, retentionSeconds: Int64) {
        self.schedule = schedule
        self.retentionSeconds = retentionSeconds
    }
}

/// One step of the access ladder; each includes everything below it.
public enum AccessRole: String, Comparable, Sendable, CaseIterable {
    case none, read, deploy, maintain, admin

    public static func < (lhs: AccessRole, rhs: AccessRole) -> Bool {
        allCases.firstIndex(of: lhs)! < allCases.firstIndex(of: rhs)!
    }
}

struct EnvironmentsResponse: Decodable {
    let environments: [ProjectEnvironment]
}
