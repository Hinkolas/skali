import Foundation

/// The external S3 location backups are written to (`BackupTarget`); the
/// secret access key is write-only and never comes back.
public struct BackupTarget: Codable, Hashable, Sendable {
    public let name: String
    public let endpoint: String
    public let region: String
    public let bucket: String
    /// Key prefix inside the bucket; empty for the root.
    public let prefix: String
    public let accessKeyId: String
    public let createdAt: Date?
    public let updatedAt: Date?

    public init(
        name: String = "default", endpoint: String, region: String = "", bucket: String, prefix: String = "",
        accessKeyId: String = "", createdAt: Date? = nil, updatedAt: Date? = nil
    ) {
        self.name = name
        self.endpoint = endpoint
        self.region = region
        self.bucket = bucket
        self.prefix = prefix
        self.accessKeyId = accessKeyId
        self.createdAt = createdAt
        self.updatedAt = updatedAt
    }

    /// The endpoint's host, e.g. "s3.eu-central-1.amazonaws.com".
    public var endpointHost: String {
        URL(string: endpoint)?.host() ?? endpoint
    }
}

/// `PUT /v1/system/backup-target`: the whole target, secret included.
public struct BackupTargetInput: Encodable, Hashable, Sendable {
    public var endpoint: String
    public var region: String
    public var bucket: String
    public var prefix: String
    public var accessKeyId: String
    public var secretAccessKey: String

    public init(
        endpoint: String = "", region: String = "", bucket: String = "", prefix: String = "",
        accessKeyId: String = "", secretAccessKey: String = ""
    ) {
        self.endpoint = endpoint
        self.region = region
        self.bucket = bucket
        self.prefix = prefix
        self.accessKeyId = accessKeyId
        self.secretAccessKey = secretAccessKey
    }

    /// Seeds an edit with the stored target; the secret starts empty.
    public init(_ target: BackupTarget) {
        self.init(
            endpoint: target.endpoint, region: target.region, bucket: target.bucket,
            prefix: target.prefix, accessKeyId: target.accessKeyId)
    }

    /// Everything the daemon requires is filled in.
    public var isComplete: Bool {
        [endpoint, bucket, accessKeyId, secretAccessKey].allSatisfy {
            !$0.trimmingCharacters(in: .whitespaces).isEmpty
        }
    }
}

/// `GET /v1/system/backup-target/summary` (`BackupTargetSummary`).
public struct BackupSummary: Codable, Hashable, Sendable {
    public struct Snapshots: Codable, Hashable, Sendable {
        public struct Project: Codable, Hashable, Sendable, Identifiable {
            public let project: String
            public let count: Int
            public let bytes: Int64
            public let newestAt: Date

            public init(project: String, count: Int, bytes: Int64, newestAt: Date) {
                self.project = project
                self.count = count
                self.bytes = bytes
                self.newestAt = newestAt
            }

            public var id: String { project }
        }

        public let count: Int
        /// What the snapshots' components hold.
        public let bytes: Int64
        public let manual: Int
        public let scheduled: Int
        /// Snapshots of environments the control plane no longer has.
        public let orphaned: Int
        public let oldestAt: Date?
        public let newestAt: Date?
        /// Largest first.
        public let projects: [Project]

        public init(
            count: Int, bytes: Int64, manual: Int, scheduled: Int, orphaned: Int,
            oldestAt: Date?, newestAt: Date?, projects: [Project]
        ) {
            self.count = count
            self.bytes = bytes
            self.manual = manual
            self.scheduled = scheduled
            self.orphaned = orphaned
            self.oldestAt = oldestAt
            self.newestAt = newestAt
            self.projects = projects
        }
    }

    public struct Run: Codable, Hashable, Sendable {
        public let project: String
        public let environment: String
        public let finishedAt: Date
        public let error: String?

        public init(project: String, environment: String, finishedAt: Date, error: String? = nil) {
            self.project = project
            self.environment = environment
            self.finishedAt = finishedAt
            self.error = error
        }
    }

    public struct Runs: Codable, Hashable, Sendable {
        /// Where the succeeded and failed counts start.
        public let since: Date
        public let succeeded: Int
        public let failed: Int
        public let backingUp: Int
        public let restoring: Int
        public let lastSucceeded: Run?
        public let lastFailed: Run?

        public init(
            since: Date, succeeded: Int, failed: Int, backingUp: Int, restoring: Int,
            lastSucceeded: Run?, lastFailed: Run?
        ) {
            self.since = since
            self.succeeded = succeeded
            self.failed = failed
            self.backingUp = backingUp
            self.restoring = restoring
            self.lastSucceeded = lastSucceeded
            self.lastFailed = lastFailed
        }
    }

    public struct Schedules: Codable, Hashable, Sendable {
        public let environments: Int
        public let scheduled: Int

        public init(environments: Int, scheduled: Int) {
            self.environments = environments
            self.scheduled = scheduled
        }
    }

    public let target: BackupTarget
    public let reachable: Bool
    public let unreachableReason: String?
    public let checkedAt: Date
    /// Nil while the bucket does not answer.
    public let snapshots: Snapshots?
    public let runs: Runs
    public let schedules: Schedules

    public init(
        target: BackupTarget, reachable: Bool, unreachableReason: String? = nil, checkedAt: Date,
        snapshots: Snapshots?, runs: Runs, schedules: Schedules
    ) {
        self.target = target
        self.reachable = reachable
        self.unreachableReason = unreachableReason
        self.checkedAt = checkedAt
        self.snapshots = snapshots
        self.runs = runs
        self.schedules = schedules
    }

    /// A failure newer than the newest success: backups need a look.
    public var failingNow: Bool {
        guard let failed = runs.lastFailed else { return false }
        guard let succeeded = runs.lastSucceeded else { return true }
        return failed.finishedAt > succeeded.finishedAt
    }
}
