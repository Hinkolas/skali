import Foundation

/// One snapshot on the backup target, read from its manifest
/// (`BackupSnapshot`).
public struct BackupSnapshot: Codable, Hashable, Sendable, Identifiable {
    public let id: String
    /// The name of the environment it was taken from.
    public let environment: String
    /// The live environment, when the source still exists.
    public let environmentId: String?
    /// No live environment owns it; project admin access is required.
    public let orphaned: Bool?
    public let createdAt: Date
    public let revisionChecksum: String
    /// `manual` snapshots stay until deleted; `scheduled` ones expire
    /// under their environment's retention.
    public let trigger: String
    public let databases: Int?
    public let buckets: Int?
    public let volumes: Int?
    public let bytes: Int64

    public init(
        id: String, environment: String, environmentId: String? = nil, orphaned: Bool? = nil,
        createdAt: Date, revisionChecksum: String = "sha256:demo", trigger: String, databases: Int? = nil,
        buckets: Int? = nil, volumes: Int? = nil, bytes: Int64
    ) {
        self.id = id
        self.environment = environment
        self.environmentId = environmentId
        self.orphaned = orphaned
        self.createdAt = createdAt
        self.revisionChecksum = revisionChecksum
        self.trigger = trigger
        self.databases = databases
        self.buckets = buckets
        self.volumes = volumes
        self.bytes = bytes
    }

    public var isScheduled: Bool { trigger == "scheduled" }
    public var isOrphaned: Bool { orphaned == true }

    /// "1 database · 2 buckets", "empty".
    public var contents: String {
        func part(_ count: Int?, _ noun: String) -> String? {
            guard let count, count > 0 else { return nil }
            return "\(count) \(noun)\(count == 1 ? "" : "s")"
        }
        let parts = [part(databases, "database"), part(buckets, "bucket"), part(volumes, "volume")].compactMap(\.self)
        return parts.isEmpty ? "empty" : parts.joined(separator: " · ")
    }

    /// The revision's checksum, short: "ee121438".
    public var shortRevision: String {
        String(revisionChecksum.replacingOccurrences(of: "sha256:", with: "").prefix(8))
    }
}

struct SnapshotsResponse: Decodable {
    let snapshots: [BackupSnapshot]
}

/// `POST /v1/environments/{id}/backups`: the run to follow.
public struct BackupStarted: Codable, Hashable, Sendable {
    public let runId: String
    public let backupId: String

    public init(runId: String, backupId: String) {
        self.runId = runId
        self.backupId = backupId
    }
}
