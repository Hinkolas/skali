import Foundation

/// One journaled run of an environment (`Run`): a deployment, rollback,
/// restart, backup and the like.
public struct Run: Codable, Hashable, Sendable, Identifiable {
    public let id: String
    /// `deployment`, `rollback`, `restart`, `reconcile`, `teardown`,
    /// `purge`, `backup`, `restore`, `restore-accept` or `rotation`.
    public let kind: String
    public let actor: String
    /// `pending`, `running`, `succeeded`, `failed` or `cancelled`.
    public let status: String
    public let createdAt: Date
    public let startedAt: Date?
    public let finishedAt: Date?
    /// One-line reason a failed run recorded.
    public let failure: String?

    public init(
        id: String, kind: String, actor: String = "ada@example.com", status: String, createdAt: Date,
        startedAt: Date? = nil, finishedAt: Date? = nil, failure: String? = nil
    ) {
        self.id = id
        self.kind = kind
        self.actor = actor
        self.status = status
        self.createdAt = createdAt
        self.startedAt = startedAt
        self.finishedAt = finishedAt
        self.failure = failure
    }

    public var isActive: Bool { status == "pending" || status == "running" }

    /// Runs that move what an environment runs.
    public var isRollout: Bool { ["deployment", "rollback", "restart"].contains(kind) }
}

struct RunsResponse: Decodable {
    let runs: [Run]
}

struct RunStarted: Decodable {
    let runId: String
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
