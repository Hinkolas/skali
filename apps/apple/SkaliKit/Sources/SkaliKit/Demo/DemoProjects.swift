import Foundation

/// The demo cluster's projects with everything their screens read:
/// environments, definitions, live status, usage, storage and runs.
/// Every number derives from the fixture table, so the list rollups and
/// the project screens agree.
enum DemoProjects {
    struct Application: Sendable {
        let key: String
        /// Nil builds from source.
        var image: String?
        var replicas = 1
        var milliCpu = 500
        var memoryMiB: Int64 = 512
        /// Edge requests per minute; nil for workloads without a route.
        var requestsPerMinute: Double?
    }

    struct Database: Sendable {
        let key: String
        var engine = "postgres"
        var version = "17"
        var sizeGiB: Int64 = 10
        var usedMiB: Double = 240
    }

    struct Bucket: Sendable {
        let key: String
        var visibility = "private"
        var quotaGiB: Int64?
        var usedMiB: Double = 0
    }

    struct Fixture: Sendable {
        let name: String
        let environments: [String]
        var applications: [Application] = []
        var databases: [Database] = []
        var buckets: [Bucket] = []
        /// Days since the last deploy.
        let days: Double
        /// The first environment's health; the others are healthy.
        var health: ServiceHealth = .healthy
        /// Environments with automatic backups.
        var backedUp: Set<String> = ["production"]

        var id: String { "demo-\(name)" }

        /// Next to production, the other environments see a fraction of
        /// its load and data.
        func load(_ environment: String) -> Double {
            environment == "production" || !environments.contains("production") ? 1 : 0.35
        }
        var activity: Date { .now.addingTimeInterval(-days * 86_400 - 600) }
    }

    static let fixtures: [Fixture] = [
        Fixture(name: "almatar-web", environments: ["production"],
                applications: [Application(key: "web", replicas: 2, requestsPerMinute: 38)], days: 42),
        Fixture(name: "birefnet-proxy", environments: ["staging"],
                applications: [Application(key: "proxy", replicas: 2, milliCpu: 1000, requestsPerMinute: 1)],
                databases: [Database(key: "jobs", usedMiB: 8.3)],
                buckets: [Bucket(key: "images", quotaGiB: 20)], days: 19),
        Fixture(name: "eplan-b1", environments: ["production"],
                applications: [Application(key: "app", image: "ghcr.io/eplan/b1:2.4.0", requestsPerMinute: 12)],
                databases: [Database(key: "main", usedMiB: 1_840)], days: 39),
        Fixture(name: "eplan-b2", environments: ["production"],
                applications: [Application(key: "app", image: "ghcr.io/eplan/b2:2.4.0", requestsPerMinute: 9)],
                databases: [Database(key: "main", usedMiB: 1_210)], days: 39),
        Fixture(name: "fuehrueber-web", environments: ["production"],
                applications: [Application(key: "web", requestsPerMinute: 6)], days: 42),
        Fixture(name: "go-lexware-automation", environments: ["production"],
                applications: [Application(key: "worker", milliCpu: 250, memoryMiB: 256)],
                databases: [Database(key: "state", sizeGiB: 5, usedMiB: 96)], days: 41),
        Fixture(name: "himmelreich", environments: ["staging", "production"],
                applications: [
                    Application(key: "api", replicas: 2, requestsPerMinute: 64),
                    Application(key: "web", replicas: 2, requestsPerMinute: 120),
                ],
                databases: [Database(key: "main", sizeGiB: 20, usedMiB: 3_400)],
                buckets: [Bucket(key: "exports", quotaGiB: 50, usedMiB: 820),
                          Bucket(key: "media", visibility: "public", usedMiB: 12_600)],
                days: 7, health: .progressing),
        Fixture(name: "kaminholz-schulz-web", environments: ["production"],
                applications: [Application(key: "web", image: "nginx:1.29-alpine", milliCpu: 100, memoryMiB: 64,
                                           requestsPerMinute: 4)], days: 42),
        Fixture(name: "lilienthal-site", environments: ["production"],
                applications: [Application(key: "site", requestsPerMinute: 22)],
                buckets: [Bucket(key: "assets", visibility: "public", usedMiB: 1_450)], days: 12),
        Fixture(name: "maja", environments: ["preview"],
                applications: [Application(key: "app", replicas: 2, requestsPerMinute: 3)], days: 2,
                health: .degraded, backedUp: []),
        Fixture(name: "nordlicht-api", environments: ["production", "staging"],
                applications: [
                    Application(key: "api", replicas: 3, milliCpu: 1000, memoryMiB: 1024, requestsPerMinute: 410),
                    Application(key: "scheduler", milliCpu: 200, memoryMiB: 256),
                    Application(key: "worker", replicas: 2, memoryMiB: 1024),
                ],
                databases: [Database(key: "analytics", sizeGiB: 50, usedMiB: 18_300),
                            Database(key: "main", sizeGiB: 20, usedMiB: 5_100)],
                days: 1),
        Fixture(name: "orbit", environments: [], days: 90),
    ]

    static func fixture(projectID: String) -> Fixture? {
        fixtures.first { $0.id == projectID }
    }

    /// Environment ids are `<project id>/<environment name>`.
    static func fixture(environmentID: String) -> (Fixture, String)? {
        let parts = environmentID.split(separator: "/", maxSplits: 1).map(String.init)
        guard parts.count == 2, let fixture = fixture(projectID: parts[0]) else { return nil }
        return (fixture, parts[1])
    }

    // MARK: Documents

    static func project(_ fixture: Fixture) -> Project {
        let activity = fixture.activity
        return Project(
            id: fixture.id, name: fixture.name, createdAt: activity.addingTimeInterval(-30 * 86_400),
            updatedAt: activity, access: ProjectAccess(role: "admin", member: true),
            summary: ProjectSummary(
                environments: environments(fixture).map { environment in
                    SummaryEnvironment(
                        id: environment.id, name: environment.name, priority: environment.settings?.priority,
                        state: environment.state, health: environment.health)
                },
                serviceCounts: ServiceCounts(
                    applications: fixture.applications.count, databases: fixture.databases.count,
                    buckets: fixture.buckets.count),
                updatedAt: activity))
    }

    static func environments(_ fixture: Fixture) -> [ProjectEnvironment] {
        fixture.environments.enumerated().map { index, name in
            ProjectEnvironment(
                id: "\(fixture.id)/\(name)", projectId: fixture.id, name: name,
                createdAt: fixture.activity.addingTimeInterval(-20 * 86_400),
                settings: EnvironmentSettings(
                    deployPolicy: name == "production" ? "promote-only" : "direct",
                    priority: name == "production" ? "high" : "normal",
                    backup: fixture.backedUp.contains(name)
                        ? BackupSchedule(schedule: "0 3 * * *", retentionSeconds: 14 * 86_400) : nil),
                state: "active", health: index == 0 ? fixture.health : .healthy,
                healthEvaluatedAt: .now.addingTimeInterval(-20))
        }
        .sorted { $0.name < $1.name }
    }

    static func draft(_ fixture: Fixture) -> ProjectDraft? {
        guard !fixture.applications.isEmpty || !fixture.databases.isEmpty || !fixture.buckets.isEmpty else {
            return nil
        }
        let mib: Int64 = 1 << 20
        let gib: Int64 = 1 << 30
        return ProjectDraft(version: 4, hash: "sha256:demo", definition: ProjectDefinition(
            applications: Dictionary(uniqueKeysWithValues: fixture.applications.map { application in
                (application.key, ApplicationSpec(
                    source: .init(kind: application.image == nil ? "build" : "image", image: application.image),
                    scaling: .init(minReplicas: application.replicas, maxReplicas: application.replicas),
                    resources: .init(limits: .init(milliCpu: application.milliCpu,
                                                   memoryBytes: application.memoryMiB * mib)),
                    routes: application.requestsPerMinute == nil ? nil : ["web": .init(path: "/")]))
            }),
            databases: Dictionary(uniqueKeysWithValues: fixture.databases.map { database in
                (database.key, DatabaseSpec(engine: database.engine, version: database.version,
                                            storageBytes: database.sizeGiB * gib))
            }),
            buckets: Dictionary(uniqueKeysWithValues: fixture.buckets.map { bucket in
                (bucket.key, BucketSpec(visibility: bucket.visibility, storageQuotaBytes: bucket.quotaGiB.map { $0 * gib }))
            })))
    }

    static func status(_ fixture: Fixture, environment: String) -> EnvironmentStatus {
        let health = environment == fixture.environments.first ? fixture.health : .healthy
        let nodes = DemoCluster.nodeNames.filter { !$0.controller }.map(\.name)
        var services: [ServiceStatus] = fixture.applications.enumerated().map { index, application in
            let troubled = index == 0 && health != .healthy
            let pods = (0..<application.replicas).map { replica in
                PodStatus(
                    name: "\(application.key)-7d9f8c-\(replica)", node: nodes[(index + replica * 3) % nodes.count],
                    ready: !(troubled && replica == application.replicas - 1),
                    restarts: troubled && health == .degraded ? 7 : 0,
                    reason: troubled && health == .degraded && replica == application.replicas - 1 ? "CrashLoopBackOff" : nil,
                    startedAt: fixture.activity)
            }
            let diagnostics = troubled && health == .degraded
                ? [HealthDiagnostic(severity: "warning", code: "pod_crash_looping",
                                    message: "1 of \(application.replicas) pods is restarting repeatedly")]
                : []
            return ServiceStatus(
                key: application.key, type: "application", health: troubled ? health : .healthy,
                diagnostics: diagnostics, pods: pods)
        }
        services += fixture.databases.map { ServiceStatus(key: $0.key, type: "database", health: .healthy) }
        services += fixture.buckets.map { ServiceStatus(key: $0.key, type: "bucket", health: .healthy) }
        let revision = RevisionRef(id: "\(fixture.id)-r12", checksum: "sha256:demo")
        return EnvironmentStatus(
            environmentId: "\(fixture.id)/\(environment)",
            targetRevision: revision, activeRevision: health == .progressing ? nil : revision,
            observation: StatusObservation(state: "fresh", lastSync: .now.addingTimeInterval(-2)),
            services: services)
    }

    static func metrics(_ fixture: Fixture, environment: String, window: NodeMetrics.Window) -> EnvironmentMetrics {
        let (count, step): (Int, Int) = switch window {
        case .hour: (60, 60)
        case .day: (96, 900)
        case .week: (168, 3600)
        }
        let end = Date.now
        let timestamps = (0..<count).map { end.addingTimeInterval(-Double((count - 1 - $0) * step)) }
        let load = fixture.load(environment)
        let applications = fixture.applications.enumerated().map { index, application in
            let phase = Double(index) + Double(fixture.name.count)
            let wave: [Double] = timestamps.map { time in
                let seconds = time.timeIntervalSince1970
                let day = sin(seconds / 86_400 * 2 * .pi + phase)
                let slow = sin(seconds / 10_800 * 2 * .pi + phase * 2) * 0.15
                return max(0.05, 0.75 + day * 0.35 + slow)
            }
            let replicas = Double(application.replicas)
            let cpu = wave.map { Int(max(1, $0 * Double(application.milliCpu) * 0.02 * replicas * load * 6)) }
            let memory = wave.map { Int64((0.22 + $0 * 0.08) * Double(application.memoryMiB) * replicas * 1_048_576) }
            let edge = application.requestsPerMinute.map { perMinute in
                // Mostly quiet with one burst, as small services see it.
                let burst = count * 3 / 4
                let requests: [Int64?] = wave.enumerated().map { bucket, value in
                    let rate = perMinute * load * (bucket == burst ? 18 : value)
                    return Int64((rate * Double(step) / 60).rounded())
                }
                return EdgeSeries(
                    requests: requests,
                    requestBytes: requests.map { $0.map { $0 * 900 } },
                    responseBytes: requests.map { $0.map { $0 * 14_000 } })
            }
            return ApplicationSeries(
                key: application.key, cpuMillicores: cpu, memoryBytes: memory, edge: edge)
        }
        return EnvironmentMetrics(window: window, stepSeconds: step, timestamps: timestamps, applications: applications)
    }

    static func storage(_ fixture: Fixture) -> [ServiceStorage] {
        let mib = 1_048_576.0
        let gib: Int64 = 1 << 30
        let sampled = Date.now.addingTimeInterval(-180)
        return fixture.environments.flatMap { environment -> [ServiceStorage] in
            let id = "\(fixture.id)/\(environment)"
            let share = fixture.load(environment)
            var services = fixture.databases.map { database in
                ServiceStorage(
                    environmentId: id, serviceKey: "databases.\(database.key)", kind: .database,
                    usedBytes: Int64(database.usedMiB * share * mib), capacityBytes: database.sizeGiB * gib,
                    sampledAt: sampled)
            }
            services += fixture.buckets.map { bucket in
                ServiceStorage(
                    environmentId: id, serviceKey: "buckets.\(bucket.key)", kind: .bucket,
                    usedBytes: Int64(bucket.usedMiB * share * mib), capacityBytes: (bucket.quotaGiB ?? 0) * gib,
                    sampledAt: sampled)
            }
            services += fixture.applications.map { application in
                ServiceStorage(
                    environmentId: id, serviceKey: application.key, kind: .temporary,
                    usedBytes: 53_248 * Int64(application.replicas), capacityBytes: Int64(application.replicas) * gib,
                    sampledAt: sampled)
            }
            return services
        }
    }
}

extension DemoCluster {
    public func environments(projectID: String) async throws -> [ProjectEnvironment] {
        try DemoProjects.environments(fixture(projectID)).map(activity.applySchedule)
    }

    public func projectDraft(projectID: String) async throws -> ProjectDraft? {
        try DemoProjects.draft(fixture(projectID))
    }

    public func projectStorage(projectID: String) async throws -> [ServiceStorage] {
        try DemoProjects.storage(fixture(projectID))
    }

    public func projectChanges(projectID: String) -> AsyncThrowingStream<ProjectChange, Error> {
        // The signal to read, then one whenever something the person
        // started settles or what they changed shows up.
        let activity = activity
        return AsyncThrowingStream { continuation in
            continuation.yield(.ready)
            let task = Task {
                var seen = activity.signature(projectID: projectID)
                while !Task.isCancelled {
                    try? await Task.sleep(for: .seconds(1))
                    let now = activity.signature(projectID: projectID)
                    if now != seen {
                        seen = now
                        continuation.yield(.changed)
                    }
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    public func environmentStatusUpdates(environmentID: String) -> AsyncThrowingStream<EnvironmentStatus, Error> {
        AsyncThrowingStream { continuation in
            guard let (fixture, environment) = DemoProjects.fixture(environmentID: environmentID) else {
                continuation.finish(throwing: Self.notFound)
                return
            }
            continuation.yield(DemoProjects.status(fixture, environment: environment))
        }
    }

    public func environmentMetrics(environmentID: String, window: NodeMetrics.Window) async throws -> EnvironmentMetrics {
        let (fixture, environment) = try fixture(environmentID: environmentID)
        return DemoProjects.metrics(fixture, environment: environment, window: window)
    }

    public func runs(environmentID: String) async throws -> [Run] {
        let (fixture, environment) = try fixture(environmentID: environmentID)
        return activity.runs(fixture, environment: environment)
    }

    public func restartEnvironment(environmentID: String) async throws -> String {
        let (fixture, environment) = try fixture(environmentID: environmentID)
        return activity.start("restart", fixture: fixture, environment: environment, actor: user.id).id
    }

    static let notFound = APIError.server(status: 404, code: "not_found", message: "not found")

    func fixture(_ projectID: String) throws -> DemoProjects.Fixture {
        guard let fixture = DemoProjects.fixture(projectID: projectID) else { throw Self.notFound }
        return fixture
    }

    func fixture(environmentID: String) throws -> (DemoProjects.Fixture, String) {
        guard let found = DemoProjects.fixture(environmentID: environmentID) else { throw Self.notFound }
        return found
    }

    // MARK: Runs

    public func run(id: String) async throws -> RunTree {
        guard let tree = activity.tree(runID: id) else { throw Self.notFound }
        return tree
    }

    public func runUpdates(id: String) -> AsyncThrowingStream<RunTree, Error> {
        let activity = activity
        return AsyncThrowingStream { continuation in
            let task = Task {
                while !Task.isCancelled {
                    guard let tree = activity.tree(runID: id) else {
                        continuation.finish(throwing: Self.notFound)
                        return
                    }
                    continuation.yield(tree)
                    if !tree.run.isActive { break }
                    try? await Task.sleep(for: .seconds(1))
                }
                continuation.finish()
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    public func cancelRun(id: String) async throws {
        activity.cancel(runID: id)
    }

    public func stepLogs(stepID: String, after cursor: String?) async throws -> LogPage {
        let logs = cursor == nil ? DemoActivity.logs(stepID: stepID) : []
        return LogPage(logs: logs, next: logs.last.map { "\($0.attempt):\($0.seq)" } ?? cursor)
    }

    public func stepLogUpdates(stepID: String, after cursor: String?) -> AsyncThrowingStream<LogEntry, Error> {
        // Demo steps write nothing more; the stream stays open until the
        // screen goes away.
        AsyncThrowingStream { _ in }
    }

    public func userDirectory() async throws -> [DirectoryUser] {
        [Self.user, Self.member].map { DirectoryUser(id: $0.id, email: $0.email, name: $0.name) }
    }

    // MARK: Backups

    public func snapshots(projectID: String) async throws -> [BackupSnapshot] {
        let fixture = try fixture(projectID)
        guard backups.current() != nil else { throw Self.noTarget }
        return activity.snapshots(fixture)
    }

    public func startBackup(environmentID: String) async throws -> BackupStarted {
        let (fixture, environment) = try fixture(environmentID: environmentID)
        guard backups.current() != nil else { throw Self.noTarget }
        guard !fixture.databases.isEmpty || !fixture.buckets.isEmpty else {
            throw APIError.server(
                status: 422, code: "nothing_to_back_up",
                message: "the active revision declares no database, bucket, or volume to snapshot")
        }
        let run = activity.start("backup", fixture: fixture, environment: environment, actor: user.id)
        return BackupStarted(runId: run.id, backupId: DemoActivity.uuid("snapshot-\(run.id)"))
    }

    public func restore(environmentID: String, snapshotID: String) async throws -> String {
        let (fixture, environment) = try fixture(environmentID: environmentID)
        guard let snapshot = activity.snapshots(fixture).first(where: { $0.id == snapshotID }) else {
            throw APIError.server(status: 404, code: "snapshot_not_found", message: "no snapshot with this id")
        }
        return activity.start("restore", fixture: fixture, environment: environment, actor: user.id, snapshot: snapshot).id
    }

    public func deleteSnapshot(projectID: String, snapshotID: String) async throws {
        _ = try fixture(projectID)
        activity.deleteSnapshot(snapshotID)
    }

    public func setBackupSchedule(environmentID: String, _ schedule: BackupSchedule?) async throws -> ProjectEnvironment {
        let (fixture, environment) = try fixture(environmentID: environmentID)
        activity.setSchedule(schedule, environmentID: environmentID)
        guard let updated = DemoProjects.environments(fixture).first(where: { $0.name == environment }) else {
            throw Self.notFound
        }
        return activity.applySchedule(updated)
    }

    private static let noTarget = APIError.server(
        status: 503, code: "backup_target_unconfigured", message: "no backup target is configured")
}
