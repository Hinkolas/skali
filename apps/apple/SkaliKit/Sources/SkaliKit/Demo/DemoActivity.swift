import Foundation
import Synchronization

/// What happens in the demo's projects: their run history and snapshots,
/// derived from the fixtures, plus what the person changed or started in
/// this launch. A run started here progresses with the clock and settles
/// after a few seconds; a backup's snapshot appears when it succeeds, a
/// deployment moves the environment to its revision, and a cancelled
/// restore holds the environment down until its data is accepted. A stop
/// takes it down until the next deployment; a delete removes it.
public final class DemoActivity: Sendable {
    private struct Started: Sendable {
        var run: Run
        let environmentID: String
        /// The snapshot a backup writes, or the one a restore reads.
        let snapshot: BackupSnapshot?
        /// The revision a deployment rolls out.
        var revision: String?
        var cancelled = false

        /// Accepting a restore's data is a single quick step.
        var length: TimeInterval { run.kind == "restore-accept" ? 1 : DemoActivity.runLength }
    }

    /// A deployment opened and not completed yet.
    private struct Pending: Sendable {
        let fixtureID: String
        let environment: String
        let revision: String
        let deployment: RunDeployment
    }

    private struct State: Sendable {
        /// Schedules set in this launch, by environment id; nil turned off.
        var schedules: [String: BackupSchedule?] = [:]
        var deletedSnapshots: Set<String> = []
        var started: [Started] = []
        var pending: [String: Pending] = [:]
    }

    /// Where an environment stands after what happened in this launch.
    struct EnvironmentState: Sendable {
        /// The revision it runs; nil while it is down.
        var revision: String?
        /// The restore holding it down.
        var hold: IncompleteRestore?
        var lastPromotionTarget: String?
        /// Deployed in this launch, so its values are current.
        var valuesCurrent = false
        /// Being deleted, and once that run settled, gone.
        var releasing = false
        var deleted = false

        var isDown: Bool { revision == nil }
    }

    private let state = Mutex(State())

    /// How long a run started in the demo takes.
    static let runLength: TimeInterval = 8

    init() {}

    // MARK: Schedules

    /// The environment with what changed in this launch: its schedule,
    /// whether it runs, and where it was last promoted to.
    func apply(_ environment: ProjectEnvironment, fixture: DemoProjects.Fixture) -> ProjectEnvironment {
        let override = state.withLock { $0.schedules[environment.id] }
        let now = environmentState(fixture, environment.name)
        let state = now.releasing ? "releasing" : now.isDown ? "down" : "active"
        var settings = environment.settings
        if let override, let current = settings {
            settings = EnvironmentSettings(
                maxRole: current.maxRole, deployPolicy: current.deployPolicy, promoteFrom: current.promoteFrom,
                priority: current.priority, backup: override)
        }
        return ProjectEnvironment(
            id: environment.id, projectId: environment.projectId, name: environment.name,
            access: environment.access, createdAt: environment.createdAt, settings: settings,
            lastPromotionTarget: now.lastPromotionTarget, state: state,
            health: state == "active" ? environment.health : .unknown, healthEvaluatedAt: environment.healthEvaluatedAt)
    }

    func setSchedule(_ schedule: BackupSchedule?, environmentID: String) {
        state.withLock { $0.schedules[environmentID] = .some(schedule) }
    }

    /// Changes when something of the project's started runs, schedules or
    /// snapshots does: what the demo's change stream watches.
    func signature(projectID: String) -> String {
        let now = Date.now
        return state.withLock { state in
            let runs = state.started.filter { $0.environmentID.hasPrefix("\(projectID)/") }
                .map { "\($0.run.id)=\(current($0, now: now).status)" }
            let schedules = state.schedules.filter { $0.key.hasPrefix("\(projectID)/") }
                .map { "\($0.key)=\($0.value?.schedule ?? "off")/\($0.value?.retentionSeconds ?? 0)" }.sorted()
            return (runs + schedules + ["\(state.deletedSnapshots.count)"]).joined(separator: ",")
        }
    }

    // MARK: Runs

    /// Starts a run of `kind` in the environment, settling on its own.
    @discardableResult
    func start(
        _ kind: String, fixture: DemoProjects.Fixture, environment: String, actor: String,
        snapshot: BackupSnapshot? = nil, only application: String? = nil,
        deployment: RunDeployment? = nil, revision: String? = nil
    ) -> Run {
        let now = Date.now
        let run = Run(
            id: Self.uuid("started-\(kind)-\(now.timeIntervalSince1970)"), kind: kind, actor: actor,
            status: "running", createdAt: now, startedAt: now,
            deployment: kind == "restart"
                ? RunDeployment(restarts: fixture.applications.filter { application == nil || $0.key == application }
                    .map { "applications.\($0.key)" })
                : deployment)
        let written = kind == "backup"
            ? BackupSnapshot(
                id: Self.uuid("snapshot-\(run.id)"), environment: environment,
                environmentId: "\(fixture.id)/\(environment)", createdAt: now, trigger: "manual",
                databases: fixture.databases.count, buckets: fixture.buckets.count,
                bytes: Self.dataBytes(fixture, environment))
            : snapshot
        state.withLock {
            $0.started.append(Started(
                run: run, environmentID: "\(fixture.id)/\(environment)", snapshot: written, revision: revision))
        }
        return run
    }

    func cancel(runID: String) {
        let now = Date.now
        state.withLock { state in
            guard let index = state.started.firstIndex(where: { $0.run.id == runID }),
                  current(state.started[index], now: now).isActive
            else { return }
            let run = state.started[index].run
            state.started[index].cancelled = true
            state.started[index].run = Run(
                id: run.id, kind: run.kind, actor: run.actor, status: "cancelled", createdAt: run.createdAt,
                startedAt: run.startedAt, finishedAt: now, deployment: run.deployment)
        }
    }

    /// A started run as it stands now.
    private func current(_ started: Started, now: Date) -> Run {
        let run = started.run
        if started.cancelled { return run }
        let elapsed = now.timeIntervalSince(run.createdAt)
        let status = elapsed < started.length ? "running" : "succeeded"
        let finished = status == "running" ? nil : run.createdAt.addingTimeInterval(min(elapsed, started.length))
        return Run(
            id: run.id, kind: run.kind, actor: run.actor, status: status, createdAt: run.createdAt,
            startedAt: run.startedAt, finishedAt: finished, deployment: run.deployment)
    }

    // MARK: Environments

    /// The revision an environment runs at launch: staging a step ahead
    /// of production, so promoting between them has something to move.
    static func initialRevision(_ fixture: DemoProjects.Fixture, _ environment: String) -> String {
        let id = "\(fixture.id)/\(environment)"
        return "sha256:" + (uuid("\(id)-revision") + uuid("\(id)-revision-tail")).filter(\.isHexDigit)
    }

    /// Replays the launch's restores, accepts and deployments over the
    /// fixture's starting point.
    func environmentState(_ fixture: DemoProjects.Fixture, _ environment: String, now: Date = .now) -> EnvironmentState {
        let id = "\(fixture.id)/\(environment)"
        var lastRevision = Self.initialRevision(fixture, environment)
        var result = EnvironmentState(
            revision: lastRevision,
            lastPromotionTarget: environment == "staging" && fixture.environments.contains("production") ? "production" : nil)
        if let hold = DemoProjects.initialHold(fixture, environment) {
            result.revision = nil
            result.hold = hold
        }
        let started = state.withLock { $0.started }.sorted { $0.run.createdAt < $1.run.createdAt }
        for entry in started where entry.environmentID.hasPrefix("\(fixture.id)/") {
            let run = current(entry, now: now)
            if run.kind == "deployment", run.status != "cancelled", run.deployment?.fromEnvironment == environment {
                result.lastPromotionTarget = String(entry.environmentID.dropFirst(fixture.id.count + 1))
            }
            guard entry.environmentID == id else { continue }
            switch run.kind {
            case "restore":
                if run.status == "succeeded" {
                    result.revision = lastRevision
                    result.hold = nil
                } else {
                    result.revision = nil
                    result.hold = IncompleteRestore(
                        id: Self.uuid("hold-\(run.id)"), runId: run.id, snapshotId: entry.snapshot?.id ?? "",
                        status: run.status, startedAt: run.createdAt, finishedAt: run.finishedAt,
                        components: fixture.databases.map { "db:\($0.key)" } + fixture.buckets.map { "bucket:\($0.key)" })
                }
            case "restore-accept":
                result.hold = nil
            case "teardown":
                result.revision = nil
            case "purge":
                result.revision = nil
                result.releasing = true
                result.deleted = run.status == "succeeded"
            case "deployment" where run.status != "cancelled":
                if let revision = entry.revision {
                    result.revision = revision
                    lastRevision = revision
                }
                result.valuesCurrent = true
            default:
                break
            }
        }
        return result
    }

    // MARK: Deploying

    /// What deploying `source` into the environment would change, as the
    /// daemon plans it.
    func plan(_ fixture: DemoProjects.Fixture, environment: String, source: DeploySource) throws -> DeployPlan {
        try planned(fixture, environment: environment, source: source).plan
    }

    private func planned(
        _ fixture: DemoProjects.Fixture, environment: String, source: DeploySource
    ) throws -> (plan: DeployPlan, revision: String, from: String?) {
        let target = environmentState(fixture, environment)
        switch source {
        case .redeploy:
            guard let revision = target.revision else { throw Self.noActiveRevision }
            let values = target.valuesCurrent ? [] : [ValueChange(name: "API_TOKEN", action: "update")]
            return (DeployPlan(plan: PlanDocument(project: fixture.name, values: values), upToDate: values.isEmpty),
                    revision, nil)
        case .promotion(let sourceID):
            guard let (sourceFixture, from) = DemoProjects.fixture(environmentID: sourceID), sourceFixture.id == fixture.id
            else { throw DemoCluster.notFound }
            guard from != environment else {
                throw APIError.server(status: 400, code: "bad_request", message: "an environment cannot be promoted to itself")
            }
            guard let revision = environmentState(fixture, from).revision else { throw Self.noActiveRevision }
            let changes: [PlanChange]
            if let current = target.revision {
                guard current != revision else {
                    return (DeployPlan(plan: PlanDocument(project: fixture.name), upToDate: true), revision, from)
                }
                changes = fixture.applications.map { application in
                    PlanChange(service: "applications.\(application.key)", action: "update",
                               detail: "artifact \(Self.short(revision)) replaces \(Self.short(current))")
                }
            } else {
                // Nothing runs there: the first revision creates everything.
                changes = fixture.applications.map { PlanChange(service: "applications.\($0.key)", action: "create") }
                    + fixture.databases.map { PlanChange(service: "databases.\($0.key)", action: "create") }
                    + fixture.buckets.map { PlanChange(service: "buckets.\($0.key)", action: "create") }
            }
            return (DeployPlan(plan: PlanDocument(project: fixture.name, changes: changes), upToDate: false), revision, from)
        }
    }

    /// Opens a deployment, refusing what the daemon refuses.
    func open(
        _ fixture: DemoProjects.Fixture, environment: String, source: DeploySource, allowDestructive: Bool
    ) throws -> OpenedDeployment {
        let id = "\(fixture.id)/\(environment)"
        if let hold = environmentState(fixture, environment).hold {
            throw APIError.server(
                status: 409, code: "restore_incomplete",
                message: "a restore stopped this environment and did not complete (restore \(hold.id)), so its data may be partial; re-run a restore, or accept the data as it is and deploy")
        }
        if runs(fixture, environment: environment).contains(where: { $0.isRollout && $0.isActive }) {
            throw APIError.server(
                status: 409, code: "deployment_in_flight", message: "another deployment is already running for this environment")
        }
        let (plan, revision, from) = try planned(fixture, environment: environment, source: source)
        if plan.isDestructive && !allowDestructive {
            throw APIError.server(
                status: 409, code: "destructive_change",
                message: "the plan contains destructive changes; review it and explicitly allow them")
        }
        if plan.upToDate { return OpenedDeployment(deployment: nil, upToDate: true) }
        let deploymentID = Self.uuid("deployment-\(id)-\(Date.now.timeIntervalSince1970)")
        let restarts = source == .redeploy ? fixture.applications.map { "applications.\($0.key)" } : nil
        let pending = Pending(
            fixtureID: fixture.id, environment: environment, revision: revision,
            deployment: RunDeployment(
                revision: revision, fromEnvironment: from, changes: plan.changes, values: plan.values, restarts: restarts))
        state.withLock { $0.pending[deploymentID] = pending }
        return OpenedDeployment(
            deployment: DeploymentRef(id: deploymentID, runId: nil),
            actions: fixture.applications.map { ArtifactAction(application: $0.key, action: "reuse", kind: "image") })
    }

    /// Hands an open deployment over; returns its run.
    func complete(deploymentID: String, actor: String) throws -> Run {
        guard let pending = state.withLock({ $0.pending.removeValue(forKey: deploymentID) }),
              let fixture = DemoProjects.fixture(projectID: pending.fixtureID)
        else { throw DemoCluster.notFound }
        return start(
            "deployment", fixture: fixture, environment: pending.environment, actor: actor,
            deployment: pending.deployment, revision: pending.revision)
    }

    func fail(deploymentID: String) {
        state.withLock { _ = $0.pending.removeValue(forKey: deploymentID) }
    }

    /// Lifts the hold the named restore keeps; the environment stays down.
    func accept(_ fixture: DemoProjects.Fixture, environment: String, restoreID: String, actor: String) throws -> Run {
        guard let hold = environmentState(fixture, environment).hold, hold.id == restoreID else {
            throw APIError.server(
                status: 409, code: "no_incomplete_restore", message: "the named restore does not hold the environment")
        }
        guard !hold.isRunning else {
            throw APIError.server(
                status: 409, code: "backup_in_flight",
                message: "the incomplete restore is still running, or its workers have not stopped yet; the environment stays held, try again in a moment")
        }
        return start("restore-accept", fixture: fixture, environment: environment, actor: actor)
    }

    /// Stops the environment, or with `purge` deletes it; whatever runs
    /// there is cancelled, as the daemon does.
    func teardown(_ fixture: DemoProjects.Fixture, environment: String, purge: Bool, actor: String) throws -> Run {
        if environmentState(fixture, environment).releasing {
            throw APIError.server(status: 409, code: "conflict", message: "environment is already releasing")
        }
        let id = "\(fixture.id)/\(environment)"
        let now = Date.now
        let running = state.withLock { state in
            state.started.filter { $0.environmentID == id && current($0, now: now).isActive }.map(\.run.id)
        }
        running.forEach(cancel)
        return start(purge ? "purge" : "teardown", fixture: fixture, environment: environment, actor: actor)
    }

    private static let noActiveRevision = APIError.server(
        status: 409, code: "conflict", message: "source environment has no active revision")

    private static func short(_ checksum: String) -> String {
        String(checksum.replacingOccurrences(of: "sha256:", with: "").prefix(12))
    }

    // MARK: Run history

    /// Every run of the environment, newest first.
    func runs(_ fixture: DemoProjects.Fixture, environment: String) -> [Run] {
        let now = Date.now
        let id = "\(fixture.id)/\(environment)"
        let started = state.withLock { $0.started }.filter { $0.environmentID == id }.map { current($0, now: now) }
        let health = environment == fixture.environments.first ? fixture.health : .healthy
        let admin = DemoCluster.user.id
        let deployed = fixture.activity
        var runs = started
        if health == .progressing {
            let begun = now.addingTimeInterval(-140)
            runs.append(Run(
                id: Self.uuid("\(id)-live"), kind: "deployment", actor: admin, status: "running", createdAt: begun,
                startedAt: begun, deployment: RunDeployment(changes: fixture.applications.prefix(1).map {
                    PlanChange(service: "applications.\($0.key)", action: "update", detail: "new image")
                })))
        }
        for snapshot in scheduledSnapshots(fixture, environment) {
            runs.append(Run(
                id: Self.uuid("backup-\(snapshot.id)"), kind: "backup", actor: "schedule:nightly", status: "succeeded",
                createdAt: snapshot.createdAt, startedAt: snapshot.createdAt,
                finishedAt: snapshot.createdAt.addingTimeInterval(95)))
        }
        if let hold = DemoProjects.initialHold(fixture, environment), let runID = hold.runId {
            runs.append(Run(
                id: runID, kind: "restore", actor: admin, status: "failed", createdAt: hold.startedAt,
                startedAt: hold.startedAt, finishedAt: hold.finishedAt, failure: hold.error))
        }
        let changes = fixture.applications.map { PlanChange(service: "applications.\($0.key)", action: "update") }
        runs.append(Run(
            id: Self.uuid("\(id)-12"), kind: "deployment", actor: admin, status: "succeeded", createdAt: deployed,
            startedAt: deployed, finishedAt: deployed.addingTimeInterval(305),
            deployment: RunDeployment(revision: Self.initialRevision(fixture, environment), changes: changes,
                                      values: [ValueChange(name: "API_TOKEN", action: "update")])))
        runs.append(Run(
            id: Self.uuid("\(id)-11"), kind: "deployment", actor: admin, status: "failed",
            createdAt: deployed.addingTimeInterval(-900), startedAt: deployed.addingTimeInterval(-900),
            finishedAt: deployed.addingTimeInterval(-606), failure: "release command for \(fixture.applications.first?.key ?? "app") exited with status 1",
            deployment: RunDeployment(changes: changes)))
        let restarted = deployed.addingTimeInterval(-6 * 86_400)
        runs.append(Run(
            id: Self.uuid("\(id)-10"), kind: "restart", actor: DemoCluster.member.id, status: "succeeded",
            createdAt: restarted, startedAt: restarted, finishedAt: restarted.addingTimeInterval(41),
            deployment: RunDeployment(restarts: fixture.applications.map { "applications.\($0.key)" })))
        return runs.sorted { $0.createdAt > $1.createdAt }
    }

    /// Finds a run of any demo project with its tree.
    func tree(runID: String) -> RunTree? {
        let startedHere = state.withLock { $0.started.contains { $0.run.id == runID } }
        for fixture in DemoProjects.fixtures {
            for environment in fixture.environments {
                if let run = runs(fixture, environment: environment).first(where: { $0.id == runID }) {
                    return RunTree(run: run, steps: Self.steps(run, fixture: fixture, startedHere: startedHere))
                }
            }
        }
        return nil
    }

    // MARK: Snapshots

    /// Every environment's snapshots, newest first.
    func snapshots(_ fixture: DemoProjects.Fixture) -> [BackupSnapshot] {
        let now = Date.now
        let (deleted, started) = state.withLock { ($0.deletedSnapshots, $0.started) }
        var snapshots = fixture.environments.flatMap { scheduledSnapshots(fixture, $0) }
        for environment in fixture.environments where fixture.backedUp.contains(environment) {
            let taken = fixture.activity.addingTimeInterval(-2 * 86_400)
            snapshots.append(BackupSnapshot(
                id: Self.uuid("\(fixture.id)/\(environment)-manual"), environment: environment,
                environmentId: "\(fixture.id)/\(environment)", createdAt: taken, trigger: "manual",
                databases: fixture.databases.count, buckets: fixture.buckets.count,
                bytes: Self.dataBytes(fixture, environment)))
        }
        if fixture.name == "nordlicht-api" {
            snapshots.append(BackupSnapshot(
                id: Self.uuid("nordlicht-preview"), environment: "preview", orphaned: true,
                createdAt: now.addingTimeInterval(-41 * 86_400), trigger: "manual", databases: 2,
                bytes: 3 << 30))
        }
        snapshots += started.filter { started in
            started.run.kind == "backup" && started.environmentID.hasPrefix("\(fixture.id)/")
                && current(started, now: now).status == "succeeded"
        }
        .compactMap(\.snapshot)
        return snapshots.filter { !deleted.contains($0.id) }.sorted { $0.createdAt > $1.createdAt }
    }

    func deleteSnapshot(_ id: String) {
        state.withLock { _ = $0.deletedSnapshots.insert(id) }
    }

    /// The past week of nightly snapshots of an environment the fixtures
    /// back up. They are history: changing the schedule now neither adds
    /// nor removes any, as on a real cluster.
    private func scheduledSnapshots(_ fixture: DemoProjects.Fixture, _ environment: String) -> [BackupSnapshot] {
        guard fixture.backedUp.contains(environment), !fixture.databases.isEmpty || !fixture.buckets.isEmpty,
              let last = ScheduleDraft().nextRun(after: .now.addingTimeInterval(-86_400))
        else { return [] }
        let latest = last > .now ? last.addingTimeInterval(-86_400) : last
        let deleted = state.withLock { $0.deletedSnapshots }
        return (0..<7).map { day in
            BackupSnapshot(
                id: Self.uuid("\(fixture.id)/\(environment)-scheduled-\(day)"), environment: environment,
                environmentId: "\(fixture.id)/\(environment)",
                createdAt: latest.addingTimeInterval(-Double(day) * 86_400), trigger: "scheduled",
                databases: fixture.databases.count, buckets: fixture.buckets.count,
                bytes: Self.dataBytes(fixture, environment) - Int64(day) * 1_400_000)
        }
        .filter { !deleted.contains($0.id) }
    }

    private static func dataBytes(_ fixture: DemoProjects.Fixture, _ environment: String) -> Int64 {
        let used = fixture.databases.map(\.usedMiB).reduce(0, +) + fixture.buckets.map(\.usedMiB).reduce(0, +)
        return Int64(used * fixture.load(environment) * 1_048_576)
    }

    // MARK: Steps

    /// The step tree a run of this kind records, its statuses following
    /// the run's: a failed deployment fails at the release command, a
    /// running one is mid-rollout. A run started in this launch reuses
    /// artifacts (the app only promotes and redeploys) and is squeezed
    /// into a few seconds.
    static func steps(_ run: Run, fixture: DemoProjects.Fixture, startedHere: Bool = false) -> [RunStep] {
        var builder = StepBuilder(run: run, scaled: startedHere)
        switch run.kind {
        case "deployment":
            builder.step("validate", "Validate project definition")
            builder.step("values", "Prepare environment values")
            builder.group("artifacts", "Prepare artifacts") { builder in
                for application in fixture.applications {
                    builder.group("artifacts.\(application.key)", application.key, service: "applications.\(application.key)") { builder in
                        if startedHere {
                            builder.step("artifacts.\(application.key).reuse", "Reuse \(Self.shortRevision(run))", seconds: 1)
                        } else if let image = application.image {
                            builder.step("artifacts.\(application.key).import", "Import \(image)", seconds: 6)
                        } else {
                            builder.step("artifacts.\(application.key).build", "Build locally (linux/arm64)", seconds: 11)
                        }
                        builder.step("artifacts.\(application.key).verify", "Verify ee1214381996", seconds: 2)
                    }
                }
            }
            builder.step("revision", "Create revision", seconds: 1)
            builder.step("promote", "Promote revision", seconds: 1)
            builder.group("rollout", "Roll out revision") { builder in
                builder.step("rollout:resources", "Apply environment resources")
                for bucket in fixture.buckets {
                    builder.step("provision:buckets.\(bucket.key)", "Provision buckets.\(bucket.key)", service: "buckets.\(bucket.key)")
                }
                for database in fixture.databases {
                    builder.step("provision:databases.\(database.key)", "Provision databases.\(database.key)", service: "databases.\(database.key)")
                }
                for application in fixture.applications.prefix(1) {
                    builder.step("release:\(application.key)", "Run release command for \(application.key)",
                                 service: "applications.\(application.key)", fails: true)
                }
                builder.step("prune", "Prune removed objects")
                for application in fixture.applications where application.requestsPerMinute != nil {
                    builder.step("tls:\(application.key)/public", "Issue TLS certificate for \(application.key) / public",
                                 service: "applications.\(application.key)")
                }
                builder.step("verify", "Verify health")
                for application in fixture.applications {
                    builder.step("apply:\(application.key)", "Apply \(application.key)", service: "applications.\(application.key)",
                                 seconds: 60, progress: Int64(application.replicas))
                }
                builder.step("activate", "Activate revision")
            }
        case "backup":
            builder.step("target", "Check the backup target")
            for database in fixture.databases {
                builder.step("dump:\(database.key)", "Dump databases.\(database.key)", service: "databases.\(database.key)", seconds: 40)
            }
            for bucket in fixture.buckets {
                builder.step("copy:\(bucket.key)", "Copy buckets.\(bucket.key)", service: "buckets.\(bucket.key)", seconds: 30)
            }
            builder.step("manifest", "Write manifest", seconds: 1)
            if run.actor.hasPrefix("schedule") { builder.step("retention", "Apply retention", seconds: 2) }
        case "restore":
            builder.step("verify", "Verify snapshot", seconds: 8)
            builder.step("stop", "Stop environment", seconds: 5)
            for (index, database) in fixture.databases.enumerated() {
                builder.step("restore:\(database.key)", "Restore databases.\(database.key)", service: "databases.\(database.key)",
                             seconds: 30, fails: index == 0)
            }
            for bucket in fixture.buckets {
                builder.step("restore:\(bucket.key)", "Restore buckets.\(bucket.key)", service: "buckets.\(bucket.key)", seconds: 20)
            }
            builder.step("resume", "Resume revision", seconds: 15)
        case "restore-accept":
            builder.step("accept", "Accept the incomplete restore's data", seconds: 0.5)
        case "teardown":
            builder.step("stop", "Scale workloads to zero", seconds: 100)
            builder.step("routes", "Release routes", seconds: 25)
        case "purge":
            builder.step("stop", "Scale workloads to zero", seconds: 30)
            for database in fixture.databases {
                builder.step("drop:\(database.key)", "Drop databases.\(database.key)", service: "databases.\(database.key)", seconds: 15)
            }
            for bucket in fixture.buckets {
                builder.step("empty:\(bucket.key)", "Delete buckets.\(bucket.key)", service: "buckets.\(bucket.key)", seconds: 15)
            }
            builder.step("namespace", "Delete the namespace and its volumes", seconds: 40)
            builder.step("environment", "Delete the environment", seconds: 10)
        default:
            let restarts = run.deployment?.restarts
            for application in fixture.applications
            where restarts?.contains("applications.\(application.key)") ?? true {
                builder.step("restart:\(application.key)", "Restart applications.\(application.key)",
                             service: "applications.\(application.key)", seconds: 20, progress: Int64(application.replicas))
            }
        }
        return builder.finish()
    }

    /// Lays steps out one after another over the run's time and gives each
    /// the status the run's outcome implies.
    private struct StepBuilder {
        let run: Run
        private var steps: [RunStep] = []
        private var clock: Date
        /// Steps after a failure are skipped; in a running run, steps after
        /// the current one wait.
        private var failed = false
        private var reachedNow = false

        /// Runs started in the demo last seconds, so their steps shrink to fit.
        private let scale: TimeInterval

        init(run: Run, scaled: Bool) {
            self.run = run
            clock = run.startedAt ?? run.createdAt
            scale = scaled ? 0.06 : 1
        }

        mutating func step(
            _ key: String, _ title: String, service: String? = nil, seconds: TimeInterval = 0.4,
            fails: Bool = false, progress: Int64? = nil
        ) {
            let start = clock
            let end = start.addingTimeInterval(seconds * scale)
            let status: String
            if failed {
                status = "skipped"
            } else if run.status == "failed" && fails {
                status = "failed"
                failed = true
            } else if run.status == "running" && (reachedNow || end > .now || key.hasPrefix("apply:")) {
                status = reachedNow ? "pending" : "running"
                reachedNow = true
            } else if run.status == "cancelled" && end > (run.finishedAt ?? .now) {
                status = "cancelled"
            } else {
                status = "succeeded"
            }
            let settled = !["pending", "running", "skipped"].contains(status)
            steps.append(RunStep(
                id: "\(run.id)/\(key)", key: key, title: title, service: service, status: status,
                progressCurrent: progress.map { status == "running" ? max(0, $0 - 1) : $0 },
                progressTotal: progress, startedAt: status == "pending" || status == "skipped" ? nil : start,
                finishedAt: settled ? end : nil))
            if status != "pending" && status != "skipped" { clock = end }
        }

        mutating func group(
            _ key: String, _ title: String, service: String? = nil, _ children: (inout StepBuilder) -> Void
        ) {
            let start = clock
            var inner = self
            inner.steps = []
            children(&inner)
            failed = inner.failed
            reachedNow = inner.reachedNow
            clock = inner.clock
            let kids = inner.steps
            let status = Self.rollUp(kids.map(\.status))
            steps.append(RunStep(
                id: "\(run.id)/\(key)", key: key, title: title, service: service, status: status,
                startedAt: kids.first?.startedAt ?? start,
                finishedAt: ["succeeded", "failed", "cancelled"].contains(status) ? clock : nil, children: kids))
        }

        private static func rollUp(_ statuses: [String]) -> String {
            for status in ["failed", "running", "pending", "cancelled"] where statuses.contains(status) {
                return status
            }
            return statuses.allSatisfy { $0 == "skipped" } && !statuses.isEmpty ? "skipped" : "succeeded"
        }

        func finish() -> [RunStep] { steps }
    }

    /// "ee121438": the revision a deployment rolls out, for its step titles.
    private static func shortRevision(_ run: Run) -> String {
        String((run.deployment?.revision ?? "").replacingOccurrences(of: "sha256:", with: "").prefix(12))
    }

    // MARK: Logs

    /// A few believable lines for a step, by what its key says it does.
    static func logs(stepID: String) -> [LogEntry] {
        let key = stepID.split(separator: "/", maxSplits: 1).last.map(String.init) ?? stepID
        let lines: [(String, String)] = switch key {
        case let key where key.hasPrefix("release:"):
            [("info", "running release command: bin/migrate"), ("info", "applying 2 migrations"),
             ("error", "migration 0007_jobs_index: relation \"jobs_status_idx\" already exists"),
             ("error", "release command exited with status 1")]
        case let key where key.hasPrefix("tls:"):
            [("info", "ordering certificate from Let's Encrypt"), ("info", "http-01 challenge answered"),
             ("info", "certificate issued, valid until Jan 1, 2027")]
        case let key where key.hasPrefix("dump:"):
            [("info", "pg_dump started"), ("info", "dump complete: 8.1 MiB compressed"), ("info", "uploaded to the backup target")]
        case let key where key.hasPrefix("apply:"):
            [("info", "rolling out 2 pods"), ("info", "pod 1/2 ready"), ("info", "pod 2/2 ready")]
        default:
            [("info", "started"), ("debug", "nothing to report"), ("info", "done")]
        }
        let start = Date.now.addingTimeInterval(-Double(lines.count) * 3)
        return lines.enumerated().map { index, line in
            LogEntry(attempt: 1, seq: Int64(index + 1), ts: start.addingTimeInterval(Double(index) * 3),
                     level: line.0, message: line.1)
        }
    }

    /// The report of a demo build, for a build step; nil for any other.
    static func buildInsights(stepID: String) -> BuildInsights? {
        let key = stepID.split(separator: "/", maxSplits: 1).last.map(String.init) ?? stepID
        guard key.hasPrefix("artifacts."), key.hasSuffix(".build") else { return nil }
        let finished = Date.now.addingTimeInterval(-60)
        let steps = [
            BuildInsightsStep(name: "[internal] load build definition from Dockerfile", status: "done"),
            BuildInsightsStep(name: "[build 1/6] FROM golang:1.26-alpine", status: "cached"),
            BuildInsightsStep(name: "[build 2/6] COPY go.mod go.sum ./", status: "cached"),
            BuildInsightsStep(name: "[build 3/6] RUN go mod download", status: "cached"),
            BuildInsightsStep(name: "[build 4/6] COPY . .", status: "done", seconds: 0.4),
            BuildInsightsStep(name: "[build 5/6] RUN go build -o /out/app ./cmd/app", status: "done", seconds: 8.6),
            BuildInsightsStep(name: "exporting to oci image format", status: "done", seconds: 1.2),
        ]
        let log = [
            "#0 building with \"desktop-linux\" instance using docker driver",
            "#1 [internal] load build definition from Dockerfile",
            "#1 DONE 0.0s",
            "#5 [build 4/6] COPY . .",
            "#5 DONE 0.4s",
            "#6 [build 5/6] RUN go build -o /out/app ./cmd/app",
            "#6 DONE 8.6s",
            "#7 exporting to oci image format",
            "#7 DONE 1.2s",
        ].joined(separator: "\n")
        return BuildInsights(
            outcome: "succeeded", engine: "docker-buildx", platform: "linux/arm64",
            startedAt: finished.addingTimeInterval(-11), finishedAt: finished, steps: steps, log: log, logLines: 9)
    }

    // MARK: Ids

    /// A stable, UUID-shaped id for a name, so demo runs and snapshots
    /// look like the daemon's.
    static func uuid(_ name: String) -> String {
        // FNV-1a, then splitmix64's finalizer so similar names differ
        // from the first digit on.
        func fnv(_ seed: UInt64) -> UInt64 {
            var hash = seed
            for byte in name.utf8 {
                hash ^= UInt64(byte)
                hash = hash &* 0x100000001b3
            }
            hash = (hash ^ (hash >> 30)) &* 0xbf58476d1ce4e5b9
            hash = (hash ^ (hash >> 27)) &* 0x94d049bb133111eb
            return hash ^ (hash >> 31)
        }
        let hex = String(format: "%016llx%016llx", fnv(0xcbf29ce484222325), fnv(0x84222325cbf29ce4))
        let characters = Array(hex)
        func slice(_ range: Range<Int>) -> String { String(characters[range]) }
        return "\(slice(0..<8))-\(slice(8..<12))-\(slice(12..<16))-\(slice(16..<20))-\(slice(20..<32))"
    }
}
