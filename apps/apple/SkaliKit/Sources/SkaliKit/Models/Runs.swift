import Foundation

/// One journaled run of an environment (`Run`): a deployment, rollback,
/// restart, backup and the like.
public struct Run: Codable, Hashable, Sendable, Identifiable {
    public let id: String
    /// `deployment`, `rollback`, `restart`, `reconcile`, `teardown`,
    /// `purge`, `backup`, `restore`, `restore-accept` or `rotation`.
    public let kind: String
    /// A user id, `schedule:<policy>` or `system:<part>`.
    public let actor: String
    /// `pending`, `running`, `succeeded`, `failed` or `cancelled`.
    public let status: String
    public let createdAt: Date
    public let startedAt: Date?
    public let finishedAt: Date?
    /// One-line reason a failed run recorded.
    public let failure: String?
    /// A deployment into a promote-only environment on an admin's bypass.
    public let bypassProtection: Bool?
    /// Routes whose TLS waits for their domain (list views only).
    public let deferredRoutes: Int?
    /// What a deployment, rollback or restart changes.
    public let deployment: RunDeployment?

    public init(
        id: String, kind: String, actor: String = "ada@example.com", status: String, createdAt: Date,
        startedAt: Date? = nil, finishedAt: Date? = nil, failure: String? = nil,
        bypassProtection: Bool? = nil, deferredRoutes: Int? = nil, deployment: RunDeployment? = nil
    ) {
        self.id = id
        self.kind = kind
        self.actor = actor
        self.status = status
        self.createdAt = createdAt
        self.startedAt = startedAt
        self.finishedAt = finishedAt
        self.failure = failure
        self.bypassProtection = bypassProtection
        self.deferredRoutes = deferredRoutes
        self.deployment = deployment
    }

    public var isActive: Bool { status == "pending" || status == "running" }

    /// Runs that move what an environment runs.
    public var isRollout: Bool { ["deployment", "rollback", "restart"].contains(kind) }
}

/// What a rollout changes (`RunDeployment`); every list is omitted when
/// empty.
public struct RunDeployment: Codable, Hashable, Sendable {
    /// Checksum of the revision the run moves the target to.
    public let revision: String?
    /// The environment a promotion copied.
    public let fromEnvironment: String?
    public let changes: [PlanChange]?
    public let values: [ValueChange]?
    /// Applications the run restarts, by dotted name.
    public let restarts: [String]?

    public init(
        revision: String? = nil, fromEnvironment: String? = nil, changes: [PlanChange]? = nil,
        values: [ValueChange]? = nil, restarts: [String]? = nil
    ) {
        self.revision = revision
        self.fromEnvironment = fromEnvironment
        self.changes = changes
        self.values = values
        self.restarts = restarts
    }
}

/// One service a plan creates, updates or removes.
public struct PlanChange: Codable, Hashable, Sendable {
    /// Dotted name, such as `applications.web`.
    public let service: String
    /// `create`, `update` or `remove`.
    public let action: String
    public let destructive: Bool?
    public let detail: String?

    public init(service: String, action: String, destructive: Bool? = nil, detail: String? = nil) {
        self.service = service
        self.action = action
        self.destructive = destructive
        self.detail = detail
    }
}

/// One value a plan changes, with the services that use it.
public struct ValueChange: Codable, Hashable, Sendable {
    public let name: String
    /// `create`, `update`, `remove` or `prune`.
    public let action: String
    public let services: [String]?

    public init(name: String, action: String, services: [String]? = nil) {
        self.name = name
        self.action = action
        self.services = services
    }
}

/// A run with its steps nested by parent (`GET /v1/runs/{id}`, and every
/// event of its stream).
public struct RunTree: Codable, Hashable, Sendable {
    public let run: Run
    public let steps: [RunStep]

    public init(run: Run, steps: [RunStep]) {
        self.run = run
        self.steps = steps
    }
}

public struct RunStep: Codable, Hashable, Sendable, Identifiable {
    public let id: String
    /// Deterministic identity within the run, such as `tls:web/public`.
    public let key: String
    public let title: String
    /// The service whose work the step is, by dotted name.
    public let service: String?
    /// `pending`, `waiting`, `running`, `succeeded`, `failed`, `skipped`
    /// or `cancelled`.
    public let status: String
    public let progressCurrent: Int64?
    public let progressTotal: Int64?
    /// When the step was first observed: a step that waits (a claim
    /// provisioning) is only stamped started once it completes.
    public let createdAt: Date?
    public let startedAt: Date?
    public let finishedAt: Date?
    public let children: [RunStep]?

    public init(
        id: String, key: String, title: String, service: String? = nil, status: String,
        progressCurrent: Int64? = nil, progressTotal: Int64? = nil, createdAt: Date? = nil, startedAt: Date? = nil,
        finishedAt: Date? = nil, children: [RunStep]? = nil
    ) {
        self.id = id
        self.key = key
        self.title = title
        self.service = service
        self.status = status
        self.progressCurrent = progressCurrent
        self.progressTotal = progressTotal
        self.createdAt = createdAt ?? startedAt
        self.startedAt = startedAt
        self.finishedAt = finishedAt
        self.children = children
    }

    public var isActive: Bool { status == "running" || status == "waiting" }

    /// A local build, `artifacts.<application>.build`: its output is in
    /// the build's insights report, not in the step's log.
    public var isBuild: Bool {
        let parts = key.split(separator: ".", omittingEmptySubsequences: false)
        return parts.count == 3 && parts[0] == "artifacts" && !parts[1].isEmpty && parts[2] == "build"
    }

    /// A skipped TLS checkpoint: the certificate waits for the domain to
    /// point here, a warning rather than an omission.
    public var isDeferredTLS: Bool { key.hasPrefix("tls:") && status == "skipped" }
}

/// One redacted step-log entry.
public struct LogEntry: Codable, Hashable, Sendable, Identifiable {
    public let attempt: Int64
    public let seq: Int64
    public let ts: Date
    /// `debug`, `info`, `warn` or `error`.
    public let level: String
    public let message: String

    public init(attempt: Int64, seq: Int64, ts: Date, level: String, message: String) {
        self.attempt = attempt
        self.seq = seq
        self.ts = ts
        self.level = level
        self.message = message
    }

    public var id: String { "\(attempt):\(seq)" }
}

/// One page of a step's log; `next` is the cursor of its last entry.
public struct LogPage: Codable, Hashable, Sendable {
    public let logs: [LogEntry]
    public let next: String?

    public init(logs: [LogEntry], next: String?) {
        self.logs = logs
        self.next = next
    }
}

/// The report a build uploads once it ended: how long it took, which of
/// its steps were cached, and the builder's redacted output.
public struct BuildInsights: Codable, Hashable, Sendable {
    /// `succeeded` or `failed`.
    public let outcome: String
    public let engine: String
    public let platform: String
    public let startedAt: Date
    public let finishedAt: Date
    /// Why the build failed.
    public let error: String?
    /// The builder's steps in the order they started.
    public let steps: [BuildInsightsStep]
    /// A log over the bound keeps its start and end (`logTruncated`).
    public let log: String
    public let logLines: Int
    public let logTruncated: Bool

    public init(
        outcome: String, engine: String, platform: String, startedAt: Date, finishedAt: Date, error: String? = nil,
        steps: [BuildInsightsStep], log: String, logLines: Int, logTruncated: Bool = false
    ) {
        self.outcome = outcome
        self.engine = engine
        self.platform = platform
        self.startedAt = startedAt
        self.finishedAt = finishedAt
        self.error = error
        self.steps = steps
        self.log = log
        self.logLines = logLines
        self.logTruncated = logTruncated
    }

    public var duration: TimeInterval { finishedAt.timeIntervalSince(startedAt) }

    public var cachedSteps: Int { steps.count { $0.status == "cached" } }

    /// The timed steps, slowest first.
    public func slowestSteps(_ count: Int = 5) -> [BuildInsightsStep] {
        Array(steps.filter { ($0.seconds ?? 0) > 0 }.sorted { ($0.seconds ?? 0) > ($1.seconds ?? 0) }.prefix(count))
    }
}

/// One builder step: a Dockerfile instruction, a context transfer, the
/// export.
public struct BuildInsightsStep: Codable, Hashable, Sendable {
    public let name: String
    /// `done`, `cached`, `error`, `canceled` or `incomplete`.
    public let status: String
    /// Absent for cached and incomplete steps.
    public let seconds: Double?

    public init(name: String, status: String, seconds: Double? = nil) {
        self.name = name
        self.status = status
        self.seconds = seconds
    }
}

/// The directory entry every account may read (`DirectoryUser`): enough
/// to put a name to the user ids runs record.
public struct DirectoryUser: Codable, Hashable, Sendable, Identifiable {
    public let id: String
    public let email: String
    public let name: String

    public init(id: String, email: String, name: String) {
        self.id = id
        self.email = email
        self.name = name
    }
}

struct RunsResponse: Decodable {
    let runs: [Run]
}

struct RunStarted: Decodable {
    let runId: String
}

struct DirectoryResponse: Decodable {
    let users: [DirectoryUser]
}

/// A signal of the project change stream; each means "re-read".
public enum ProjectChange: String, Sendable {
    /// Sent on every connect: what changed while disconnected is unknown.
    case ready
    case changed
    /// The project is gone; the stream ends.
    case deleted
    /// The caller lost access; the stream ends.
    case revoked
}
