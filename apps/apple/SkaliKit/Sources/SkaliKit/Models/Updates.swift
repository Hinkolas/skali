import Foundation

/// The update channel a cluster follows.
public enum ReleaseChannel: String, Codable, Hashable, Sendable, CaseIterable, Identifiable {
    case stable, beta

    public var id: String { rawValue }
}

/// `GET /v1/system/updates` (`UpdateStatus`): what runs, what the feed
/// found, the operator's settings, the nodes, and the running or last update.
public struct UpdateStatus: Codable, Hashable, Sendable {
    public struct Installed: Codable, Hashable, Sendable {
        /// The running daemon's version.
        public let version: String
        /// The release the cluster state records.
        public let platformVersion: String?
        /// The running release's page; absent for a development build.
        public let url: String?

        public init(version: String, platformVersion: String? = nil, url: String? = nil) {
            self.version = version
            self.platformVersion = platformVersion
            self.url = url
        }
    }

    /// The daemon's one-line verdict, which the screens follow.
    public struct Summary: Codable, Hashable, Sendable {
        public enum State: String, Codable, Hashable, Sendable {
            case unknown, notChecked = "not_checked", noRelease = "no_release", current, available
            case incomplete, updating, failed

            public init(from decoder: any Decoder) throws {
                let raw = try decoder.singleValueContainer().decode(String.self)
                self = State(rawValue: raw) ?? .unknown
            }
        }

        /// What the operator can do next.
        public enum Action: String, Codable, Hashable, Sendable {
            case none = "", update, finish, retry

            public init(from decoder: any Decoder) throws {
                let raw = try decoder.singleValueContainer().decode(String.self)
                self = Action(rawValue: raw) ?? .none
            }
        }

        public struct Progress: Codable, Hashable, Sendable {
            public let done: Int
            public let total: Int
            public let percent: Int
            public let phase: String

            public init(done: Int, total: Int, percent: Int, phase: String) {
                self.done = done
                self.total = total
                self.percent = percent
                self.phase = phase
            }
        }

        public let state: State
        public let convergedVersion: String?
        public let targetVersion: String?
        public let action: Action
        public let detail: String?
        public let progress: Progress

        public init(
            state: State, convergedVersion: String? = nil, targetVersion: String? = nil,
            action: Action = .none, detail: String? = nil,
            progress: Progress = Progress(done: 0, total: 0, percent: 0, phase: "Preparing")
        ) {
            self.state = state
            self.convergedVersion = convergedVersion
            self.targetVersion = targetVersion
            self.action = action
            self.detail = detail
            self.progress = progress
        }

        /// What an older daemon without a summary reads as.
        static let unreported = Summary(
            state: .unknown,
            detail: "This platform has not reported cluster-wide update status yet.")
    }

    public let installed: Installed
    public let summary: Summary
    public let channel: ReleaseChannel
    public let autoUpdate: Bool
    public let lastCheckedAt: Date?
    /// Why the last scan did not complete; absent when it did.
    public let lastError: String?
    /// `offline`, `not_found`, `rate_limited`, `unavailable` or `invalid`.
    public let lastErrorKind: String?
    /// The newest release on the channel, newer than what runs or not.
    public let latest: Release?
    public let updateAvailable: Bool
    /// A cluster coordinator exists to perform updates.
    public let managed: Bool
    /// An update could start right now.
    public let manageable: Bool
    /// Why an update cannot start, when it cannot.
    public let reason: String?
    public let nodes: [UpdateNode]
    /// The running update, or the last one.
    public let operation: UpdateOperation?
    public let lastSuccessful: UpdateOperation?

    public init(
        installed: Installed, summary: Summary, channel: ReleaseChannel, autoUpdate: Bool,
        lastCheckedAt: Date? = nil, lastError: String? = nil, lastErrorKind: String? = nil,
        latest: Release? = nil, updateAvailable: Bool = false, managed: Bool = true,
        manageable: Bool = true, reason: String? = nil, nodes: [UpdateNode] = [],
        operation: UpdateOperation? = nil, lastSuccessful: UpdateOperation? = nil
    ) {
        self.installed = installed
        self.summary = summary
        self.channel = channel
        self.autoUpdate = autoUpdate
        self.lastCheckedAt = lastCheckedAt
        self.lastError = lastError
        self.lastErrorKind = lastErrorKind
        self.latest = latest
        self.updateAvailable = updateAvailable
        self.managed = managed
        self.manageable = manageable
        self.reason = reason
        self.nodes = nodes
        self.operation = operation
        self.lastSuccessful = lastSuccessful
    }

    public init(from decoder: any Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        installed = try container.decode(Installed.self, forKey: .installed)
        // A daemon from before the summary existed still decodes.
        summary = try container.decodeIfPresent(Summary.self, forKey: .summary) ?? .unreported
        channel = try container.decode(ReleaseChannel.self, forKey: .channel)
        autoUpdate = try container.decode(Bool.self, forKey: .autoUpdate)
        lastCheckedAt = try container.decodeIfPresent(Date.self, forKey: .lastCheckedAt)
        lastError = try container.decodeIfPresent(String.self, forKey: .lastError)
        lastErrorKind = try container.decodeIfPresent(String.self, forKey: .lastErrorKind)
        latest = try container.decodeIfPresent(Release.self, forKey: .latest)
        updateAvailable = try container.decodeIfPresent(Bool.self, forKey: .updateAvailable) ?? false
        managed = try container.decodeIfPresent(Bool.self, forKey: .managed) ?? false
        manageable = try container.decodeIfPresent(Bool.self, forKey: .manageable) ?? false
        reason = try container.decodeIfPresent(String.self, forKey: .reason)
        nodes = try container.decodeIfPresent([UpdateNode].self, forKey: .nodes) ?? []
        operation = try container.decodeIfPresent(UpdateOperation.self, forKey: .operation)
        lastSuccessful = try container.decodeIfPresent(UpdateOperation.self, forKey: .lastSuccessful)
    }
}

/// A node as the update document reports it: what each component runs.
public struct UpdateNode: Codable, Hashable, Sendable, Identifiable {
    /// Immutable enrollment identity; hostnames can be reused.
    public let id: String
    public let name: String
    /// `server` (a controller) or `agent` (a worker).
    public let role: String
    public let k3sVersion: String?
    public let agentVersion: String?
    public let coordinatorVersion: String?
    public let coordinatorLastSeen: Date?
    public let phase: String
    public let lastSeen: Date?

    public init(
        id: String, name: String, role: String, k3sVersion: String? = nil,
        agentVersion: String? = nil, coordinatorVersion: String? = nil,
        coordinatorLastSeen: Date? = nil, phase: String = "active", lastSeen: Date? = nil
    ) {
        self.id = id
        self.name = name
        self.role = role
        self.k3sVersion = k3sVersion
        self.agentVersion = agentVersion
        self.coordinatorVersion = coordinatorVersion
        self.coordinatorLastSeen = coordinatorLastSeen
        self.phase = phase
        self.lastSeen = lastSeen
    }

    public var isController: Bool { role == "server" }
}

/// A cluster operation as the coordinator journals it (`UpdateOperation`):
/// its phase, and one step per node, controllers first in the order they run.
public struct UpdateOperation: Codable, Hashable, Sendable, Identifiable {
    public struct Step: Codable, Hashable, Sendable {
        public enum Phase: String, Codable, Hashable, Sendable {
            case pending, running, complete, failed

            public init(from decoder: any Decoder) throws {
                let raw = try decoder.singleValueContainer().decode(String.self)
                self = Phase(rawValue: raw) ?? .pending
            }
        }

        public let nodeId: String
        public let node: String
        public let action: String
        public let phase: Phase
        public let error: String?

        public init(nodeId: String, node: String, action: String = "upgrade", phase: Phase, error: String? = nil) {
            self.nodeId = nodeId
            self.node = node
            self.action = action
            self.phase = phase
            self.error = error
        }
    }

    public let id: String
    /// `pending`, `upgrading-nodes`, `reconciling-platform`, `verifying`,
    /// `complete`, `failed`, or another coordinator phase.
    public let phase: String
    public let targetVersion: String?
    public let fromVersion: String?
    public let startedAt: Date
    public let updatedAt: Date
    public let completedAt: Date?
    public let error: String?
    public let steps: [Step]

    public init(
        id: String, phase: String, targetVersion: String? = nil, fromVersion: String? = nil,
        startedAt: Date, updatedAt: Date, completedAt: Date? = nil, error: String? = nil,
        steps: [Step] = []
    ) {
        self.id = id
        self.phase = phase
        self.targetVersion = targetVersion
        self.fromVersion = fromVersion
        self.startedAt = startedAt
        self.updatedAt = updatedAt
        self.completedAt = completedAt
        self.error = error
        self.steps = steps
    }
}

/// `PUT /v1/system/updates/settings`.
public struct UpdateSettings: Codable, Hashable, Sendable {
    public var channel: ReleaseChannel
    public var autoUpdate: Bool

    public init(channel: ReleaseChannel, autoUpdate: Bool) {
        self.channel = channel
        self.autoUpdate = autoUpdate
    }
}
