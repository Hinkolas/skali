import Foundation
import Testing
@testable import SkaliKit

struct ProjectOverviewTests {
    // MARK: Decoding

    @Test func decodesTheEnvironmentList() throws {
        let json = """
        {"environments":[
          {"id":"e1","project_id":"p","name":"production","previous_names":["prod"],"access":"admin",
           "created_at":"2026-09-01T10:00:00Z",
           "settings":{"max_role":"read","deploy_policy":"promote-only","promote_from":[],"priority":"high",
             "backup":{"schedule":"0 3 * * *","retention_seconds":1209600,"strategy":"complete"}},
           "state":"active","health":"healthy","health_evaluated_at":"2026-10-23T10:00:00Z"},
          {"id":"e2","project_id":"p","name":"staging","access":"none"}]}
        """
        let environments = try JSONCoding.decoder.decode(EnvironmentsResponse.self, from: Data(json.utf8)).environments
        #expect(environments[0].settings?.backup?.retentionSeconds == 1_209_600)
        #expect(environments[0].isHighPriority)
        #expect(environments[0].answers(to: "prod"))
        #expect(environments[1].isLocked)
        #expect(environments[1].state == nil)
    }

    @Test func decodesTheStatusDocument() throws {
        let json = """
        {"environment_id":"e1","state":"active","target_revision":{"id":"r","checksum":"c"},"active_revision":null,
         "observation":{"state":"fresh","stale_since":null,"last_sync":"2026-10-23T10:00:00Z"},"platforms":["linux/arm64"],
         "services":[
           {"key":"web","type":"application","health":"healthy","diagnostics":[],
            "pods":[{"name":"web-1","node":"n1","phase":"Running","ready":true,"restarts":0,"started_at":null,"serving":true}],
            "routes":[{"key":"public","domain":"example.com","path":"/","tls":"automatic","strategy":"round-robin","compress":true,
              "edge":{"state":"unresolved","checked_at":null,"addresses":[],"deferred":true},
              "aliases":[{"domain":"www.example.com","edge":{"state":"unresolved","checked_at":null,"addresses":[],"deferred":true}}]}]},
           {"key":"main","type":"database","health":"rebooting","diagnostics":[],"pods":[]}],
         "incomplete_restore":null}
        """
        let status = try JSONCoding.decoder.decode(EnvironmentStatus.self, from: Data(json.utf8))
        #expect(status.service(.application, "web")?.pods.first?.ready == true)
        #expect(status.service(.database, "main")?.health == .unknown)
        #expect(status.deferredRoutes == 2)
        #expect(status.activeRevision == nil)
    }

    @Test func decodesTheDraftWithItsCamelCaseDefinition() throws {
        let json = """
        {"draft":{"version":3,"format":"yaml","source":"name: demo","hash":"h","definition":{
          "version":"1","name":"demo",
          "applications":{"web_app":{"source":{"kind":"build","build":{"context":".","dockerfile":"Dockerfile"}},
            "scaling":{"minReplicas":2,"maxReplicas":4},"resources":{"limits":{"milliCpu":500,"memoryBytes":536870912}},
            "routes":{"public":{"domain":{"parts":[]},"path":"/","port":{"name":"http"},"tls":"automatic"}}}},
          "databases":{"main":{"engine":"postgres","version":"17","isolation":"shared","availability":"standard","storageBytes":10737418240}},
          "buckets":{"media":{"visibility":"public","versioning":"disabled"}}}}}
        """
        let draft = try JSONCoding.decoder.decode(DraftResponse.self, from: Data(json.utf8)).draft
        let application = try #require(draft.definition.applications?["web_app"])
        #expect(application.scaling.minReplicas == 2)
        #expect(application.resources?.limits?.memoryBytes == 536_870_912)
        #expect(draft.definition.databases?["main"]?.storageBytes == 10_737_418_240)
        #expect(draft.definition.serviceCount == 3)
    }

    @Test func decodesMetricsStorageAndRuns() throws {
        let metrics = try JSONCoding.decoder.decode(EnvironmentMetrics.self, from: Data("""
        {"window":"24h","step_seconds":900,"timestamps":["2026-10-23T10:00:00Z","2026-10-23T10:15:00Z"],
         "applications":[{"key":"web","cpu_millicores":[3,null],"memory_bytes":[100,null],
           "edge":{"requests":[1,2],"request_bytes":[10,20],"response_bytes":[30,null]}}]}
        """.utf8))
        #expect(metrics.applications[0].edge?.responseBytes == [30, nil])

        let storage = try JSONCoding.decoder.decode(ProjectStorageResponse.self, from: Data("""
        {"services":[{"environment_id":"e1","service_key":"databases.main","kind":"database","used_bytes":null,
          "capacity_bytes":1024,"sampled_at":"2026-10-23T10:00:00Z"}]}
        """.utf8)).services
        #expect(storage[0].serviceName == "main")
        #expect(storage[0].footprint == 1024)

        let runs = try JSONCoding.decoder.decode(RunsResponse.self, from: Data("""
        {"runs":[{"id":"r2","kind":"backup","project_id":"p","environment_id":"e1","actor":"schedule:nightly",
           "status":"succeeded","created_at":"2026-10-23T03:00:00Z","started_at":null,"finished_at":null,"bypass_protection":false},
          {"id":"r1","kind":"deployment","project_id":"p","environment_id":"e1","actor":"ada@example.com",
           "status":"failed","failure":"release command failed","created_at":"2026-10-22T03:00:00Z","started_at":null,
           "finished_at":"2026-10-22T03:02:00Z","bypass_protection":false,"deployment":{"revision":"x"}}]}
        """.utf8)).runs
        #expect(runs.latestRollout?.id == "r1")
        #expect(runs.latestRollout?.failure == "release command failed")
    }

    // MARK: Event streams

    @Test func parsesServerSentEvents() {
        var parser = ServerSentEventParser()
        let lines = [": ping", "event: status", "data: {\"a\":1}", "", "data:{}", "id: 7", "event: runs", "data: []"]
        let events = lines.compactMap { parser.consume($0[...]) }
        #expect(events == [
            ServerSentEvent(name: "status", data: "{\"a\":1}"),
            ServerSentEvent(name: "message", data: "{}"),
            ServerSentEvent(name: "runs", data: "[]"),
        ])
    }

    // MARK: Rollups

    @Test func prefersTheNamedThenProductionThenAnOpenEnvironment() {
        let locked = ProjectEnvironment(id: "1", projectId: "p", name: "alpha", access: "none")
        let staging = ProjectEnvironment(id: "2", projectId: "p", name: "staging", previousNames: ["stage"])
        let production = ProjectEnvironment(id: "3", projectId: "p", name: "production")
        #expect([locked, staging, production].preferred(named: nil)?.id == "3")
        #expect([locked, staging, production].preferred(named: "stage")?.id == "2")
        #expect([locked, staging].preferred(named: "gone")?.id == "2")
        #expect([locked].preferred(named: nil)?.id == "1")
        #expect([ProjectEnvironment]().preferred(named: nil) == nil)
    }

    @Test func listsServicesWithTheirLiveState() {
        let definition = ProjectDefinition(
            applications: [
                "web": ApplicationSpec(source: .init(kind: "build"), scaling: .init(minReplicas: 1, maxReplicas: 3)),
                "api": ApplicationSpec(source: .init(kind: "image", image: "x"), scaling: .init(minReplicas: 1, maxReplicas: 1)),
            ],
            databases: ["main": DatabaseSpec(engine: "postgres", version: "17")],
            buckets: ["media": BucketSpec(visibility: "private")])
        #expect(definition.serviceItems(status: nil).map(\.state) == Array(repeating: .pending, count: 4))

        let status = EnvironmentStatus(environmentId: "e", services: [
            ServiceStatus(key: "web", type: "application", health: .healthy,
                          pods: [PodStatus(name: "a", node: "n"), PodStatus(name: "b", node: "n", ready: false)]),
            ServiceStatus(key: "main", type: "database", health: .degraded),
        ])
        let items = definition.serviceItems(status: status)
        #expect(items.map(\.key) == ["api", "web", "main", "media"])
        #expect(items[0].state == .notDeployed)
        #expect(items[1].detailLine == "build · 1/2")
        #expect(items[2].detailLine == "postgres 17")
        #expect(items[2].state == .health(.degraded))
        #expect(items[3].detailLine == "private")
    }

    @Test func summarizesTheEnvironment() {
        let status = EnvironmentStatus(environmentId: "e", state: "down", services: [
            ServiceStatus(key: "a", type: "application", health: .healthy),
            ServiceStatus(key: "b", type: "database", health: .unhealthy),
        ])
        #expect(environmentSummary(serviceCount: 3, status: status) == "3 services · 1/2 healthy · stopped")
        #expect(environmentSummary(serviceCount: 1, status: nil) == "1 service")
        #expect(status.overallHealth == .unhealthy)
        #expect(EnvironmentStatus(environmentId: "e", services: []).overallHealth == .unknown)
    }

    @Test func scalesLimitsByRunningPods() {
        let definition = ProjectDefinition(applications: [
            "web": ApplicationSpec(
                source: .init(kind: "build"), scaling: .init(minReplicas: 1, maxReplicas: 4),
                resources: .init(limits: .init(milliCpu: 500, memoryBytes: 256))),
            "worker": ApplicationSpec(
                source: .init(kind: "build"), scaling: .init(minReplicas: 2, maxReplicas: 2),
                resources: .init(limits: .init(milliCpu: 250))),
            "free": ApplicationSpec(source: .init(kind: "build"), scaling: .init(minReplicas: 1, maxReplicas: 1)),
        ])
        let status = EnvironmentStatus(environmentId: "e", services: [
            ServiceStatus(key: "web", type: "application", health: .healthy,
                          pods: (0..<3).map { PodStatus(name: "\($0)", node: "n") }),
        ])
        let limits = UsageLimits(definition: definition, status: status)
        #expect(limits.cpuMillicores == 3 * 500 + 2 * 250)
        #expect(limits.memoryBytes == Int64(3 * 256))
        #expect(UsageLimits(definition: nil, status: nil) == UsageLimits())
    }

    @Test func buildsTheUsageTiles() {
        let metrics = EnvironmentMetrics(
            window: .day, stepSeconds: 900,
            timestamps: (0..<3).map { Date(timeIntervalSince1970: Double($0) * 900) },
            applications: [
                ApplicationSeries(
                    key: "web", cpuMillicores: [8, 12, nil], memoryBytes: [100 << 20, 120 << 20, nil],
                    edge: EdgeSeries(requests: [15, 315, 15], requestBytes: [1024, nil, 0], responseBytes: [nil, 2048, 0])),
                ApplicationSeries(key: "worker", cpuMillicores: [2, 2, 1], memoryBytes: [10 << 20, nil, 8 << 20]),
            ])
        let stats = metrics.usageStats(limits: UsageLimits(cpuMillicores: 2000, memoryBytes: 1 << 30))
        let requests = stats[0], cpu = stats[1], memory = stats[2], traffic = stats[3]
        #expect(requests.value == "1")
        #expect(requests.unit == "/min")
        #expect(requests.footnote == "peak 21/min")
        // The newest bucket only the worker filled is current usage.
        #expect(cpu.value == "0.00")
        #expect(cpu.unit == "/ 2 cores")
        #expect(cpu.footnote == "peak 0.01")
        #expect(memory.value == "8")
        #expect(memory.unit == "MiB / 1 GiB")
        #expect(traffic.value == "3")
        #expect(traffic.unit == "KiB / 24h")
        #expect(traffic.footnote == "in 1 KiB · out 2 KiB")

        let quiet = EnvironmentMetrics(window: .day, stepSeconds: 900, timestamps: [.now], applications: [])
        #expect(quiet.usageStats(limits: UsageLimits()).allSatisfy { $0.value == nil })
    }

    @Test func formatsCounts() {
        #expect(formatCount(0.04) == "0")
        #expect(formatCount(0.36) == "0.4")
        #expect(formatCount(2.97) == "3")
        #expect(formatCount(950) == "950")
        #expect(formatCount(1_240) == "1.2k")
        #expect(formatCount(34_200) == "34k")
        #expect(formatCount(2_100_000) == "2.1M")
    }

    @Test func rollsUpOneEnvironmentsStorage() {
        let sampled = Date.now
        let storage = StorageRollup(storage: [
            ServiceStorage(environmentId: "e1", serviceKey: "databases.jobs", kind: .database, usedBytes: 800,
                           capacityBytes: 10_000, sampledAt: sampled),
            ServiceStorage(environmentId: "e1", serviceKey: "proxy", kind: .volume, usedBytes: nil,
                           capacityBytes: 1_000, sampledAt: sampled),
            ServiceStorage(environmentId: "e1", serviceKey: "buckets.images", kind: .bucket, usedBytes: 0,
                           capacityBytes: 0, sampledAt: sampled),
            ServiceStorage(environmentId: "e2", serviceKey: "databases.jobs", kind: .database, usedBytes: 5,
                           capacityBytes: 10, sampledAt: sampled),
        ], environmentID: "e1")
        #expect(storage.services.map(\.serviceName) == ["proxy", "jobs", "images"])
        #expect(storage.total == 1_800)
        #expect(storage.capacity == 11_000)
        #expect(storage.byKind.map(\.kind) == [.volume, .database])
    }

    @Test func namesBackupCadences() {
        func cadence(_ cron: String) -> String { BackupSchedule(schedule: cron, retentionSeconds: 60).cadence }
        #expect(cadence("0 3 * * *") == "daily")
        #expect(cadence("15 * * * *") == "hourly")
        #expect(cadence("0 */6 * * *") == "every 6 hours")
        #expect(cadence("0 3 * * 0") == "weekly")
        #expect(cadence("0 3 1 * *") == "monthly")
        #expect(cadence("0 3 * 1 *") == "scheduled")
    }

    // MARK: Demo

    @Test func theDemoProjectMatchesItsListing() async throws {
        let demo = DemoCluster()
        let project = try #require(DemoCluster.projects.first { $0.name == "birefnet-proxy" })
        let environments = try await demo.environments(projectID: project.id)
        let environment = try #require(environments.preferred(named: nil))
        let draft = try #require(try await demo.projectDraft(projectID: project.id))
        #expect(draft.definition.serviceCount == project.serviceCount)

        var statuses = demo.environmentStatusUpdates(environmentID: environment.id).makeAsyncIterator()
        let status = try #require(try await statuses.next())
        #expect(environmentSummary(serviceCount: draft.definition.serviceCount, status: status)
                == "3 services · 3/3 healthy · active")
        let storage = StorageRollup(storage: try await demo.projectStorage(projectID: project.id), environmentID: environment.id)
        #expect(formatBytes(storage.capacity) == "32 GiB")
        let runs = try await demo.runs(environmentID: environment.id)
        #expect(runs.latestRollout?.status == "succeeded")
        #expect(environment.settings?.backup == nil)
    }
}
