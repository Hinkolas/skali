import Foundation
import Testing
@testable import SkaliKit

struct RunAndBackupTests {
    // MARK: Decoding

    @Test func decodesARunTree() throws {
        let json = """
        {"run":{"id":"01a101fc-0000-0000-0000-000000000000","kind":"deployment","project_id":"p","environment_id":"e",
           "actor":"u1","status":"succeeded","created_at":"2026-10-03T15:38:00Z","started_at":"2026-10-03T15:38:00Z",
           "finished_at":"2026-10-03T15:43:05Z","bypass_protection":false,
           "deployment":{"revision":"sha256:ee1214381996","changes":[{"service":"applications.proxy","action":"update"}],
             "values":[{"name":"TOKEN","action":"update","services":["applications.proxy"]}]}},
         "steps":[{"id":"s1","key":"artifacts","title":"Prepare artifacts","status":"succeeded",
           "created_at":"2026-10-03T15:38:00Z","started_at":"2026-10-03T15:38:00Z","finished_at":"2026-10-03T15:38:13Z",
           "attempts":[{"id":"a","number":1,"status":"succeeded","started_at":"2026-10-03T15:38:00Z","finished_at":null}],
           "children":[{"id":"s2","key":"tls:proxy/public","title":"Issue TLS certificate","service":"applications.proxy",
             "status":"skipped","progress_current":1,"progress_total":2,"created_at":"2026-10-03T15:38:00Z",
             "started_at":null,"finished_at":null,"attempts":[]}]}]}
        """
        let tree = try JSONCoding.decoder.decode(RunTree.self, from: Data(json.utf8))
        #expect(tree.run.duration() == "5m 5s")
        #expect(tree.run.shortID == "01a101fc")
        #expect(tree.steps[0].children?.first?.isDeferredTLS == true)
        #expect(tree.deferredRoutes == 1)
        #expect(tree.progress == 1)
        #expect(tree.run.changeSummary == "proxy updated · 1 value changed")
    }

    @Test func decodesLogsSnapshotsAndTheDirectory() throws {
        let page = try JSONCoding.decoder.decode(LogPage.self, from: Data("""
        {"logs":[{"attempt":1,"seq":4,"ts":"2026-10-03T15:38:00.25Z","level":"error","message":"boom","fields":{"code":1}}],
         "next":"1:4"}
        """.utf8))
        #expect(page.logs.first?.id == "1:4")

        let snapshots = try JSONCoding.decoder.decode(SnapshotsResponse.self, from: Data("""
        {"snapshots":[{"id":"s","environment":"production","environment_id":"e","created_at":"2026-10-03T03:00:00Z",
          "revision_checksum":"sha256:ee1214381996aa","encryption":"none","trigger":"scheduled","strategy":"complete",
          "databases":1,"buckets":2,"volumes":0,"bytes":1024},
          {"id":"o","environment":"preview","orphaned":true,"created_at":"2026-09-03T03:00:00Z",
          "revision_checksum":"sha256:aa","encryption":"none","trigger":"manual","strategy":"complete","bytes":0}]}
        """.utf8)).snapshots
        #expect(snapshots[0].contents == "1 database · 2 buckets")
        #expect(snapshots[0].shortRevision == "ee121438")
        #expect(snapshots[0].isScheduled)
        #expect(snapshots[1].isOrphaned)
        #expect(snapshots[1].contents == "empty")

        let users = try JSONCoding.decoder.decode(DirectoryResponse.self, from: Data("""
        {"users":[{"id":"u1","email":"ada@example.com","name":"Ada","role":"admin","created_at":"2026-01-01T00:00:00Z"}]}
        """.utf8)).users
        #expect(users.first?.name == "Ada")
    }

    @Test func turningASchedulOffSendsNull() throws {
        let off = try JSONCoding.encoder.encode(BackupSchedulePatch(backup: nil))
        #expect(String(decoding: off, as: UTF8.self) == #"{"backup":null}"#)
        let on = try JSONCoding.encoder.encode(BackupSchedulePatch(
            backup: BackupSchedule(schedule: "0 3 * * *", retentionSeconds: 604_800)))
        #expect(String(decoding: on, as: UTF8.self).contains(#""retention_seconds":604800"#))
    }

    // MARK: Runs

    @Test func summarizesWhatARolloutChanged() {
        func run(_ kind: String, _ deployment: RunDeployment?) -> Run {
            Run(id: "r", kind: kind, status: "succeeded", createdAt: .now, deployment: deployment)
        }
        #expect(run("backup", nil).changeSummary == nil)
        #expect(run("deployment", RunDeployment()).changeSummary == "no changes")
        let promoted = RunDeployment(
            fromEnvironment: "staging",
            changes: [
                PlanChange(service: "databases.main", action: "create"),
                PlanChange(service: "applications.web", action: "update"),
                PlanChange(service: "applications.worker", action: "update"),
            ],
            values: [ValueChange(name: "A", action: "update"), ValueChange(name: "A", action: "prune")])
        #expect(run("deployment", promoted).changeSummary
                == "from staging · web, worker updated · main created · 1 value changed")
        #expect(run("rollback", RunDeployment(revision: "sha256:0123456789ab")).changeSummary
                == "to 01234567 · no changes")
        let many = RunDeployment(restarts: ["applications.a", "applications.b", "applications.c", "applications.d"])
        #expect(run("restart", many).changeSummary == "a, b and 2 more restarted")
    }

    @Test func namesActorsAndKinds() {
        let directory = ["2b7c1e44-0000-4000-8000-000000000000": DirectoryUser(
            id: "2b7c1e44-0000-4000-8000-000000000000", email: "ada@example.com", name: "Ada")]
        #expect(describeActor("2b7c1e44-0000-4000-8000-000000000000", directory: directory) == "Ada")
        #expect(describeActor("9f1d2c33-0000-4000-8000-000000000000", directory: directory) == "9f1d2c33")
        #expect(describeActor("schedule:nightly", directory: [:]) == "schedule")
        #expect(describeActor("system:reconcile", directory: [:]) == "system")
        #expect(Run(id: "r", kind: "teardown", status: "running", createdAt: .now).kindLabel == "stop")
        #expect(!Run(id: "r", kind: "purge", status: "running", createdAt: .now).isCancellable)
        #expect(Run(id: "r", kind: "deployment", status: "pending", createdAt: .now).isCancellable)
    }

    @Test func formatsDurations() {
        let start = Date(timeIntervalSince1970: 0)
        #expect(formatDuration(from: start, to: start.addingTimeInterval(42)) == "42s")
        #expect(formatDuration(from: start, to: start.addingTimeInterval(305)) == "5m 5s")
        #expect(formatDuration(from: start, to: start.addingTimeInterval(4_320)) == "1h 12m")
    }

    @Test func groupsRunsByDayAndFilters() {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "UTC")!
        let day = Date(timeIntervalSince1970: 86_400 * 100)
        let runs = [
            Run(id: "c", kind: "backup", status: "running", createdAt: day.addingTimeInterval(90_000)),
            Run(id: "b", kind: "deployment", status: "succeeded", createdAt: day.addingTimeInterval(3_600)),
            Run(id: "a", kind: "teardown", status: "pending", createdAt: day.addingTimeInterval(60)),
        ]
        let groups = runs.groupedByDay(calendar: calendar)
        #expect(groups.map { $0.runs.map(\.id) } == [["c"], ["b", "a"]])
        #expect(runs.filter(RunFilter.rollouts.includes).map(\.id) == ["b", "a"])
        #expect(runs.filter(RunFilter.backups.includes).map(\.id) == ["c"])
        #expect(runs.activeRun?.id == "c")
        #expect(runs.lastSucceededDeployment?.id == "b")
    }

    // MARK: Schedules

    @Test func describesSchedules() {
        #expect(describeCron("0 3 * * *") == "daily at 03:00")
        #expect(describeCron("30 */6 * * *") == "every 6 hours")
        #expect(describeCron("5 * * * *") == "hourly at :05")
        #expect(describeCron("0 3 * * 0") == "weekly on Sunday at 03:00")
        #expect(describeCron("0 4 2 * *") == "monthly on the 2nd at 04:00")
        #expect(describeCron("0 3 1-5 * *") == "0 3 1-5 * *")
        #expect(describeRetention(seconds: 1_209_600) == "14 days")
        #expect(describeRetention(seconds: 129_600) == "36 hours")
        #expect(describeRetention(seconds: 2_700) == "45 minutes")
    }

    @Test func editsSchedulesInShapes() {
        #expect(ScheduleDraft().cron == "0 3 * * *")
        #expect(ScheduleDraft().retentionSeconds == 604_800)
        for cron in ["15 * * * *", "0 */6 * * *", "0 */12 * * *", "30 2 * * *", "0 4 * * 1", "0 4 15 * *"] {
            let draft = ScheduleDraft(schedule: BackupSchedule(schedule: cron, retentionSeconds: 60))
            #expect(draft.repeatEvery != .custom, "\(cron)")
            #expect(draft.cron == cron)
        }
        var custom = ScheduleDraft(schedule: BackupSchedule(schedule: "0 3 1-5 * *", retentionSeconds: 60))
        #expect(custom.repeatEvery == .custom)
        #expect(custom.cron == "0 3 1-5 * *")
        custom.expression = "0 3"
        #expect(!custom.isValid)
        var weekly = ScheduleDraft()
        weekly.repeatEvery = .weekly
        weekly.weekday = 0
        #expect(weekly.cron == "0 3 * * 0")
        #expect(ScheduleDraft(schedule: BackupSchedule(schedule: "0 3 * * 7", retentionSeconds: 60)).weekday == 0)
    }

    @Test func findsTheNextRunInUTC() throws {
        let noon = try #require(ISO8601DateFormatter().date(from: "2026-10-21T12:00:00Z"))  // a Wednesday
        let daily = ScheduleDraft(schedule: BackupSchedule(schedule: "0 3 * * *", retentionSeconds: 60))
        #expect(daily.nextRun(after: noon) == ISO8601DateFormatter().date(from: "2026-10-22T03:00:00Z"))
        let sixHourly = ScheduleDraft(schedule: BackupSchedule(schedule: "30 */6 * * *", retentionSeconds: 60))
        #expect(sixHourly.nextRun(after: noon) == ISO8601DateFormatter().date(from: "2026-10-21T12:30:00Z"))
        let weekly = ScheduleDraft(schedule: BackupSchedule(schedule: "0 3 * * 0", retentionSeconds: 60))
        #expect(weekly.nextRun(after: noon) == ISO8601DateFormatter().date(from: "2026-10-25T03:00:00Z"))
        let custom = ScheduleDraft(schedule: BackupSchedule(schedule: "0 3 1-5 * *", retentionSeconds: 60))
        #expect(custom.nextRun(after: noon) == nil)
    }

    // MARK: Demo

    @Test func theDemoBacksUpRestoresAndSchedules() async throws {
        let demo = DemoCluster()
        let project = try #require(DemoCluster.projects.first { $0.name == "birefnet-proxy" })
        let environment = try #require(try await demo.environments(projectID: project.id).first)
        #expect(environment.settings?.backup == nil)
        #expect(try await demo.snapshots(projectID: project.id).isEmpty)

        let started = try await demo.startBackup(environmentID: environment.id)
        let tree = try await demo.run(id: started.runId)
        #expect(tree.run.kind == "backup")
        #expect(tree.run.isActive)
        #expect(tree.steps.map(\.key).contains("dump:jobs"))
        #expect(try await demo.runs(environmentID: environment.id).first?.id == started.runId)

        let schedule = BackupSchedule(schedule: "0 2 * * *", retentionSeconds: 3 * 86_400)
        let updated = try await demo.setBackupSchedule(environmentID: environment.id, schedule)
        #expect(updated.settings?.backup == schedule)
        _ = try await demo.setBackupSchedule(environmentID: environment.id, nil)
        #expect(try await demo.environments(projectID: project.id).first?.settings?.backup == nil)

        // A project with history: a week of nightly snapshots and a manual one.
        let backedUp = try #require(DemoCluster.projects.first { $0.name == "eplan-b1" })
        let production = try #require(try await demo.environments(projectID: backedUp.id).first)
        let snapshots = try await demo.snapshots(projectID: backedUp.id)
        #expect(snapshots.filter(\.isScheduled).count == 7)
        #expect(snapshots.count == 8)
        try await demo.deleteSnapshot(projectID: backedUp.id, snapshotID: snapshots[0].id)
        #expect(try await demo.snapshots(projectID: backedUp.id).count == 7)
        let restore = try await demo.restore(environmentID: production.id, snapshotID: snapshots[1].id)
        #expect(try await demo.run(id: restore).run.kind == "restore")
    }

    @Test func theDemoFailedDeploymentFailsAtItsReleaseCommand() async throws {
        let demo = DemoCluster()
        let project = try #require(DemoCluster.projects.first { $0.name == "birefnet-proxy" })
        let environment = try #require(try await demo.environments(projectID: project.id).first)
        let failed = try #require(try await demo.runs(environmentID: environment.id).first { $0.status == "failed" })
        let tree = try await demo.run(id: failed.id)
        let rollout = try #require(tree.steps.first { $0.key == "rollout" })
        #expect(rollout.status == "failed")
        #expect(rollout.children?.first { $0.key == "release:proxy" }?.status == "failed")
        #expect(rollout.children?.last?.status == "skipped")
        let logs = try await demo.stepLogs(stepID: "\(failed.id)/release:proxy", after: nil)
        #expect(logs.logs.last?.level == "error")
    }
}

struct ServiceProgressTests {
    private func step(
        _ key: String, service: String? = nil, status: String = "succeeded", created: Double = 0, finished: Double? = 1,
        children: [RunStep]? = nil
    ) -> RunStep {
        RunStep(
            id: key, key: key, title: key, service: service, status: status,
            createdAt: Date(timeIntervalSince1970: created), startedAt: Date(timeIntervalSince1970: created),
            finishedAt: finished.map { Date(timeIntervalSince1970: $0) }, children: children)
    }

    private func tree(_ steps: [RunStep]) -> RunTree {
        RunTree(run: Run(id: "r", kind: "deployment", status: "running", createdAt: .now), steps: steps)
    }

    @Test func groupsStepsByServiceWithTheEnvironmentLast() throws {
        let progress = try #require(tree([
            step("validate"),
            step("artifacts", children: [
                step("artifacts:web", service: "applications.web", created: 1, finished: 9, children: [
                    step("build", created: 1, finished: 8),
                    step("verify", created: 8, finished: 9),
                ]),
            ]),
            step("rollout", status: "running", finished: nil, children: [
                step("provision:main", service: "databases.main", created: 10, finished: 12),
                step("apply:web", service: "applications.web", status: "running", created: 12, finished: nil),
                step("cache", service: "caches.redis", status: "pending", created: 0, finished: nil),
            ]),
            step("activate", status: "pending", finished: nil),
        ]).serviceProgress)
        #expect(progress.map(\.name) == ["web", "main", "caches.redis", "environment"])
        let web = progress[0]
        #expect(web.kind == .application)
        #expect(web.steps.map(\.key) == ["build", "verify", "apply:web"])
        #expect(web.status == "running")
        #expect(web.current.key == "apply:web")
        #expect(web.finishedAt == nil)
        #expect(progress[1].status == "succeeded")
        #expect(progress[1].finishedAt == Date(timeIntervalSince1970: 12))
        #expect(progress[2].kind == nil)
        #expect(progress[3].steps.map(\.key) == ["validate", "activate"])
        #expect(progress[3].status == "pending")
    }

    @Test func runsWithoutServicesHaveOnlyTheTree() {
        #expect(tree([step("target"), step("manifest")]).serviceProgress == nil)
    }

    @Test func findsTheCurrentStepAndCountsLeaves() {
        let running = tree([
            step("a"),
            step("rollout", status: "running", finished: nil, children: [
                step("b"), step("c", status: "running", finished: nil), step("d", status: "pending", finished: nil),
            ]),
        ])
        #expect(running.currentStep?.key == "c")
        #expect(running.leafCounts.done == 2)
        #expect(running.leafCounts.total == 4)
    }
}
