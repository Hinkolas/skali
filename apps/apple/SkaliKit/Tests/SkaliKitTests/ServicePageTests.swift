import Foundation
import Testing
@testable import SkaliKit

struct ServicePageTests {
    @Test func decodesAnApplicationsRuntimeAndDependencies() throws {
        let json = """
        {"applications":{"web":{"source":{"kind":"image","image":"nginx:1.29"},"command":["nginx","-g","daemon off;"],
           "ports":{"http":{"port":8080,"protocol":"http"},"metrics":{"port":9090,"protocol":"http"}},
           "health":{"readiness":{"http":{"port":{"name":"http"},"path":"/healthz"}}},
           "scaling":{"minReplicas":1,"maxReplicas":4},"deployment":{"rollout":{"strategy":"rolling"}}},
          "worker":{"source":{"kind":"build"},"scaling":{"minReplicas":2,"maxReplicas":2}}},
         "databases":{"main":{"engine":"postgres","version":"17","isolation":"shared","availability":"single"}},
         "buckets":{"media":{"visibility":"public","versioning":"disabled"}},
         "dependencies":{"applications.web":["databases.main","buckets.media"],"applications.worker":["databases.main"]}}
        """
        let definition = try JSONCoding.decoder.decode(ProjectDefinition.self, from: Data(json.utf8))
        let web = try #require(definition.applications?["web"])
        #expect(web.sourceLabel == "nginx:1.29")
        #expect(web.portsLabel == "http 8080/http · metrics 9090/http")
        #expect(web.healthCheckLabel == "http /healthz")
        #expect(web.rolloutStrategy == "rolling")
        #expect(web.scaleLabel == "1–4")
        let worker = try #require(definition.applications?["worker"])
        #expect(worker.rolloutStrategy == "blue-green")
        #expect(worker.portsLabel == nil)
        #expect(worker.scaleLabel == "2")
        #expect(definition.dependencies(of: "web").map(\.key) == ["main", "media"])
        #expect(definition.dependents(of: .database, "main") == ["web", "worker"])
        #expect(definition.dependents(of: .bucket, "media") == ["web"])
    }

    @Test func decodesConnectionsLogsAndRoutes() throws {
        let connection = try JSONCoding.decoder.decode(DatabaseConnection.self, from: Data("""
        {"service":"main","phase":"Ready","engine":"postgres","major":17,"isolation":"shared","availability":"single",
         "host":"pg.svc","port":5432,"database":"app_main","credential_version":2}
        """.utf8))
        #expect(connection.isProvisioned)
        let pending = try JSONCoding.decoder.decode(DatabaseConnection.self, from: Data("""
        {"service":"main","phase":"Provisioning","engine":"postgres","major":17,"isolation":"dedicated","availability":"ha"}
        """.utf8))
        #expect(!pending.isProvisioned)

        let line = try JSONCoding.decoder.decode(RuntimeLogLine.self, from: Data("""
        {"service":"web","pod":"web-1","line":"GET /","time":"2026-10-24T10:00:00Z","previous":true}
        """.utf8))
        #expect(line.previous == true)

        let route = try JSONCoding.decoder.decode(RouteStatus.self, from: Data("""
        {"key":"public","domain":"example.com","path":"/app","tls":"automatic","strategy":"round-robin","compress":true,
         "certificate":{"name":"c","secret_name":"s","state":"active","not_after":"2027-01-01T00:00:00Z","renewal_time":null},
         "aliases":[{"domain":"www.example.com"}]}
        """.utf8))
        #expect(route.url?.absoluteString == "https://example.com/app")
        #expect(route.certificate?.state == "active")
        #expect(RouteStatus(key: "k", domain: "local.test", path: "/").url?.absoluteString == "http://local.test")
    }

    @Test func tellsFailingPodsFromStartingOnes() {
        #expect(!PodStatus(name: "a", node: "n", phase: "Pending", ready: false, reason: "ContainerCreating").isFailing)
        #expect(PodStatus(name: "a", node: "n", ready: false, reason: "CrashLoopBackOff").isFailing)
        #expect(PodStatus(name: "a", node: "n", phase: "Failed", ready: false).isFailing)
        #expect(PodStatus(name: "a", node: "n", ready: false, reason: "CrashLoopBackOff").stateLabel == "crashloopbackoff")
    }

    @Test func aWorkerShowsReplicasAndRestartsInsteadOfTraffic() {
        let metrics = EnvironmentMetrics(
            window: .day, stepSeconds: 900, timestamps: [.now],
            applications: [
                ApplicationSeries(key: "worker", cpuMillicores: [100], memoryBytes: [64 << 20]),
                ApplicationSeries(key: "web", cpuMillicores: [900], memoryBytes: [512 << 20]),
            ])
        let worker = ApplicationSpec(
            source: .init(kind: "build"), scaling: .init(minReplicas: 2, maxReplicas: 3),
            resources: .init(limits: .init(milliCpu: 500)))
        let status = ServiceStatus(key: "worker", type: "application", health: .healthy, pods: [
            PodStatus(name: "a", node: "n", restarts: 2), PodStatus(name: "b", node: "n", ready: false, restarts: 1),
        ])
        let stats = metrics.applicationStats(key: "worker", spec: worker, status: status)
        #expect(stats.map(\.kind) == [.replicas, .cpu, .memory, .restarts])
        #expect(stats[0].value == "1")
        #expect(stats[0].unit == "/ 2 ready")
        #expect(stats[0].footnote == "scale 2–3")
        // Only this application's series, against its own limit for two pods.
        #expect(stats[1].value == "0.1")
        #expect(stats[1].unit == "/ 1 core")
        #expect(stats[3].value == "3")

        let web = ApplicationSpec(
            source: .init(kind: "build"), scaling: .init(minReplicas: 1, maxReplicas: 1), routes: ["public": .init(path: "/")])
        #expect(metrics.applicationStats(key: "web", spec: web, status: nil).map(\.kind) == [.requests, .cpu, .memory, .traffic])
    }

    @Test func findsWhatEachRunDidToAService() {
        let runs = [
            Run(id: "3", kind: "restart", status: "succeeded", createdAt: .now,
                deployment: RunDeployment(restarts: ["applications.web"])),
            Run(id: "2", kind: "deployment", status: "succeeded", createdAt: .now,
                deployment: RunDeployment(values: [ValueChange(name: "TOKEN", action: "update", services: ["applications.web"])])),
            Run(id: "1", kind: "deployment", status: "succeeded", createdAt: .now,
                deployment: RunDeployment(changes: [PlanChange(service: "databases.main", action: "create")])),
            Run(id: "0", kind: "backup", status: "succeeded", createdAt: .now),
        ]
        let web = runs.changes(to: "applications.web")
        #expect(web.map(\.run.id) == ["3", "2"])
        #expect(web[0].action == "restart")
        #expect(web[1].action == "update")
        #expect(web[1].detail == "value TOKEN changed")
        #expect(runs.changes(to: "databases.main").map(\.action) == ["create"])
        #expect(runs.lastSucceededBackup?.id == "0")
    }

    @Test func theDemoServesConnectionsLogsAndRestarts() async throws {
        let demo = DemoCluster()
        let project = try #require(DemoCluster.projects.first { $0.name == "birefnet-proxy" })
        let environment = try #require(try await demo.environments(projectID: project.id).first)
        let connection = try await demo.databaseConnection(environmentID: environment.id, key: "jobs")
        #expect(connection.isProvisioned)
        let credentials = try await demo.revealDatabaseCredentials(environmentID: environment.id, key: "jobs")
        #expect(credentials.url.hasPrefix("postgres://"))
        var logs = demo.runtimeLogs(environmentID: environment.id, service: "proxy").makeAsyncIterator()
        #expect(try await logs.next()?.service == "proxy")
        let restart = try await demo.restartApplication(environmentID: environment.id, key: "proxy")
        let tree = try await demo.run(id: restart)
        #expect(tree.run.deployment?.restarts == ["applications.proxy"])
        let draft = try #require(try await demo.projectDraft(projectID: project.id))
        #expect(draft.definition.dependents(of: .database, "jobs") == ["proxy"])
    }
}
