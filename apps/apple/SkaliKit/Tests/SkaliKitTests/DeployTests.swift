import Foundation
import Synchronization
import Testing
@testable import SkaliKit

struct DeployTests {
    // MARK: Decoding and encoding

    @Test func decodesAPlanAndTheOpenedDeployment() throws {
        let plan = try JSONCoding.decoder.decode(DeployPlan.self, from: Data("""
        {"warnings":[{"code":"health_check_missing","message":"web has no health check","paths":["applications.web"]}],
         "plan":{"project":"demo","changes":[
           {"service":"applications.web","action":"update","detail":"artifact aa replaces bb"},
           {"service":"databases.old","action":"remove","destructive":true,"detail":"drops the database and its data"}],
           "values":[{"name":"API_TOKEN","action":"update"}]},
         "actions":[{"application":"web","action":"reuse","kind":"image","artifact_id":"a","build_id":"",
           "upstream":"","platform":"linux/arm64","reference":"r"}],
         "up_to_date":false,"required_role":"deploy","bypass_protection":false}
        """.utf8))
        #expect(plan.changes.count == 2)
        #expect(plan.values.first?.name == "API_TOKEN")
        #expect(plan.isDestructive)
        #expect(plan.warnings?.first?.code == "health_check_missing")

        let opened = try JSONCoding.decoder.decode(OpenedDeployment.self, from: Data("""
        {"deployment":{"id":"d1","project_id":"p","environment_id":"e","definition_version_id":"v","status":"preparing",
          "run_id":"r1","build_executor":"","bypass_protection":false},
         "plan":{"project":"demo"},"actions":[],"up_to_date":false,"required_role":"deploy","bypass_protection":false}
        """.utf8))
        #expect(opened.deployment == DeploymentRef(id: "d1", runId: "r1"))

        let upToDate = try JSONCoding.decoder.decode(OpenedDeployment.self, from: Data("""
        {"up_to_date":true,"plan":{"project":"demo"},"required_role":"deploy","bypass_protection":false}
        """.utf8))
        #expect(upToDate.deployment == nil)
        #expect(upToDate.upToDate == true)
    }

    @Test func requestsNameTheirSourceOnly() throws {
        func json(_ request: DeployRequest) throws -> NSDictionary {
            object(String(decoding: try JSONCoding.encoder.encode(request), as: UTF8.self))
        }
        // The daemon refuses unknown fields, so a plan never carries
        // allow_destructive.
        #expect(try json(DeployRequest(.promotion(fromEnvironmentID: "e1"))) == ["from_environment_id": "e1"])
        #expect(try json(DeployRequest(.redeploy)) == ["redeploy": true])
        #expect(try json(DeployRequest(.redeploy, allowDestructive: true)) == ["allow_destructive": true, "redeploy": true])
    }

    @Test func readsWhatAnIncompleteRestoreRewrote() throws {
        let restore = try JSONCoding.decoder.decode(IncompleteRestore.self, from: Data("""
        {"id":"h","run_id":null,"snapshot_id":"s","status":"cancelled","started_at":"2026-10-03T03:00:00Z",
         "finished_at":"2026-10-03T03:04:00Z","components":["db:main","bucket:media","volume:web.data"]}
        """.utf8))
        #expect(restore.outcome == "was cancelled")
        #expect(!restore.isRunning)
        #expect(restore.restoredComponents.map(\.kind) == [.database, .bucket, .application])
        #expect(restore.restoredComponents.map(\.label) == ["main", "media", "web/data"])
    }

    // MARK: What may be deployed where

    @Test func explainsWhichTargetsRefuseAPromotion() {
        func environment(_ name: String, access: String = "admin", state: String = "active",
                         policy: String = "direct", from: [String] = [], last: String? = nil) -> ProjectEnvironment {
            ProjectEnvironment(
                id: name, projectId: "p", name: name, access: access,
                settings: EnvironmentSettings(deployPolicy: policy, promoteFrom: from),
                lastPromotionTarget: last, state: state)
        }
        let staging = environment("staging", last: "production")
        #expect(environment("production", policy: "promote-only").promotionRefusal(from: staging) == nil)
        #expect(environment("production", policy: "promote-only", from: ["staging"]).promotionRefusal(from: staging) == nil)
        #expect(environment("production", policy: "promote-only", from: ["qa", "canary"]).promotionRefusal(from: staging)
            == "accepts promotions from qa or canary only")
        #expect(environment("preview", access: "read").promotionRefusal(from: staging) == "deploy access required")
        #expect(environment("preview", access: "none").promotionRefusal(from: staging) == "locked for you")
        #expect(environment("old", state: "releasing").promotionRefusal(from: staging) == "being deleted")

        let targets = staging.promotionTargets(among: [
            environment("locked", access: "none"), staging, environment("qa"), environment("production"),
        ])
        #expect(targets.map(\.name) == ["production", "qa", "locked"])
        #expect(promotionRefusal(source: staging, environments: [staging], status: nil) == "no other environments")
        let down = EnvironmentStatus(environmentId: "staging", state: "down", services: [])
        #expect(promotionRefusal(source: staging, environments: targets, status: down) == "nothing is running yet")
        #expect(promotionRefusal(source: staging, environments: targets, status: nil) == nil)
    }

    @Test func explainsWhyARedeployCannotStart() {
        let environment = ProjectEnvironment(id: "e", projectId: "p", name: "production")
        let running = EnvironmentStatus(
            environmentId: "e", activeRevision: RevisionRef(id: "r", checksum: "sha256:ee1214381996"), services: [])
        #expect(redeployRefusal(environment, status: running, runs: []) == nil)
        #expect(redeployRefusal(environment, status: nil, runs: nil) == nil)
        #expect(redeployRefusal(environment, status: EnvironmentStatus(environmentId: "e", state: "down", services: []), runs: nil)
            == "nothing is running yet")
        let held = EnvironmentStatus(
            environmentId: "e", state: "down", services: [],
            incompleteRestore: IncompleteRestore(id: "h", runId: nil, snapshotId: "s", status: "failed", startedAt: .now))
        #expect(redeployRefusal(environment, status: held, runs: nil) == "held by a restore")
        let reader = ProjectEnvironment(id: "e", projectId: "p", name: "production", access: "read")
        #expect(redeployRefusal(reader, status: running, runs: nil) == "deploy access required")
        let backup = Run(id: "b", kind: "backup", actor: "u", status: "running", createdAt: .now)
        #expect(redeployRefusal(environment, status: running, runs: [backup]) == "a backup is running")
        #expect(running.activeRevision?.shortChecksum == "ee121438")
    }

    // MARK: Starting a deployment

    @Test func failsADeploymentThatWouldBuild() async throws {
        let host = "stray.test"
        let requests = StubProtocol.serve(host) { request in
            switch request.url?.path() {
            case "/api/v1/environments/e1/deployments":
                (201, #"{"deployment":{"id":"d1","run_id":"r1"},"actions":[{"application":"web","action":"build"}],"up_to_date":false}"#)
            case "/api/v1/deployments/d1/fail":
                (200, #"{"status":"failed"}"#)
            default:
                (404, #"{"error":{"code":"not_found","message":"not found"}}"#)
            }
        }
        let client = APIClient(address: ClusterAddress(apiURL: URL(string: "https://\(host)/api")!), session: StubProtocol.session)
        await #expect(throws: DeployError.unexpectedArtifactWork(application: "web", action: "build")) {
            try await client.deploy(into: "e1", from: .promotion(fromEnvironmentID: "e0"))
        }
        #expect(requests().map(\.path) == ["/api/v1/environments/e1/deployments", "/api/v1/deployments/d1/fail"])
        #expect(requests().first.map { object($0.body) } == ["from_environment_id": "e0"])
    }

    @Test func completesADeploymentThatOnlyReuses() async throws {
        let host = "reuse.test"
        let requests = StubProtocol.serve(host) { request in
            switch request.url?.path() {
            case "/api/v1/environments/e1/deployments":
                (201, #"{"deployment":{"id":"d1"},"actions":[{"application":"web","action":"reuse"}],"up_to_date":false}"#)
            case "/api/v1/deployments/d1/complete":
                (202, #"{"run_id":"r9"}"#)
            default:
                (404, #"{"error":{"code":"not_found","message":"not found"}}"#)
            }
        }
        let client = APIClient(address: ClusterAddress(apiURL: URL(string: "https://\(host)/api")!), session: StubProtocol.session)
        let outcome = try await client.deploy(into: "e1", from: .redeploy, allowDestructive: true)
        #expect(outcome == .started(runID: "r9"))
        #expect(requests().first.map { object($0.body) } == ["allow_destructive": true, "redeploy": true])
    }

    // MARK: The demo

    @Test func theDemoRedeploys() async throws {
        let demo = DemoCluster()
        let environments = try await demo.environments(projectID: "demo-himmelreich")
        let staging = try #require(environments.first { $0.name == "staging" })
        let production = try #require(environments.first { $0.name == "production" })
        #expect(staging.lastPromotionTarget == "production")
        #expect(production.isProtected)

        // Production's values moved since its last deploy.
        let redeploy = try await demo.planDeployment(environmentID: production.id, from: .redeploy)
        #expect(!redeploy.upToDate)
        #expect(redeploy.values.map(\.name) == ["API_TOKEN"])
        guard case .started = try await demo.deploy(into: production.id, from: .redeploy) else {
            Issue.record("the redeploy did not start")
            return
        }
        #expect(try await demo.planDeployment(environmentID: production.id, from: .redeploy).upToDate)
        // While it runs, nothing else deploys there.
        await #expect(throws: APIError.self) {
            try await demo.deploy(into: production.id, from: .promotion(fromEnvironmentID: staging.id))
        }
    }

    @Test func theDemoPromotes() async throws {
        let demo = DemoCluster()
        let environments = try await demo.environments(projectID: "demo-himmelreich")
        let staging = try #require(environments.first { $0.name == "staging" })
        let production = try #require(environments.first { $0.name == "production" })

        // Staging runs a newer revision; promoting it moves production on.
        let promotion = try await demo.planDeployment(
            environmentID: production.id, from: .promotion(fromEnvironmentID: staging.id))
        #expect(promotion.changes.map(\.service) == ["applications.api", "applications.web"])
        guard case .started(let runID) = try await demo.deploy(
            into: production.id, from: .promotion(fromEnvironmentID: staging.id))
        else {
            Issue.record("the promotion did not start")
            return
        }
        let tree = try await demo.run(id: runID)
        #expect(tree.run.deployment?.fromEnvironment == "staging")
        #expect(tree.steps.flattenedKeys.contains("artifacts:api:reuse"))
        let stagingStatus = try await demo.environmentStatus(environmentID: staging.id)
        #expect(try await demo.environmentStatus(environmentID: production.id).activeRevision?.checksum
            == stagingStatus.activeRevision?.checksum)
        #expect(try await demo.planDeployment(
            environmentID: production.id, from: .promotion(fromEnvironmentID: staging.id)).upToDate)

        // Staging is mid-rollout and refuses another deployment.
        await #expect(throws: APIError.self) {
            try await demo.deploy(into: staging.id, from: .promotion(fromEnvironmentID: production.id))
        }
    }

    @Test func theDemoHoldsAnEnvironmentAfterAFailedRestore() async throws {
        let demo = DemoCluster()
        let environments = try await demo.environments(projectID: "demo-nordlicht-api")
        let staging = try #require(environments.first { $0.name == "staging" })
        let production = try #require(environments.first { $0.name == "production" })
        #expect(staging.state == "down")

        let status = try await demo.environmentStatus(environmentID: staging.id)
        let hold = try #require(status.incompleteRestore)
        #expect(hold.restoredComponents.map(\.label) == ["analytics", "main"])
        #expect(redeployRefusal(staging, status: status, runs: nil) == "held by a restore")
        let runs = try await demo.runs(environmentID: staging.id)
        #expect(runs.contains { $0.id == hold.runId && $0.status == "failed" })

        // Deploys are refused until the data is accepted.
        await #expect(throws: APIError.self) {
            try await demo.deploy(into: staging.id, from: .promotion(fromEnvironmentID: production.id))
        }
        await #expect(throws: APIError.self) {
            try await demo.acceptRestore(environmentID: staging.id, restoreID: "another")
        }
        _ = try await demo.acceptRestore(environmentID: staging.id, restoreID: hold.id)
        let accepted = try await demo.environmentStatus(environmentID: staging.id)
        #expect(accepted.incompleteRestore == nil)
        #expect(accepted.state == "down")

        // Nothing runs there, so a promotion creates every service.
        let plan = try await demo.planDeployment(
            environmentID: staging.id, from: .promotion(fromEnvironmentID: production.id))
        #expect(plan.changes.allSatisfy { $0.action == "create" })
        _ = try await demo.deploy(into: staging.id, from: .promotion(fromEnvironmentID: production.id))
        #expect(try await demo.environmentStatus(environmentID: staging.id).state == "active")
    }

    @Test func aCancelledRestoreHoldsTheEnvironment() async throws {
        let demo = DemoCluster()
        let production = try #require(try await demo.environments(projectID: "demo-eplan-b1").first)
        let snapshot = try #require(try await demo.snapshots(projectID: "demo-eplan-b1").first)
        let runID = try await demo.restore(environmentID: production.id, snapshotID: snapshot.id)
        #expect(try await demo.environmentStatus(environmentID: production.id).incompleteRestore?.isRunning == true)
        try await demo.cancelRun(id: runID)
        let held = try #require(try await demo.environmentStatus(environmentID: production.id).incompleteRestore)
        #expect(held.status == "cancelled")
        #expect(held.restoredComponents.map(\.label) == ["main"])
    }
}

/// A JSON object, compared regardless of key order.
private func object(_ text: String) -> NSDictionary {
    (try? JSONSerialization.jsonObject(with: Data(text.utf8))) as? NSDictionary ?? [:]
}

private extension [RunStep] {
    var flattenedKeys: [String] {
        flatMap { [$0.key] + ($0.children ?? []).flattenedKeys }
    }
}

/// Answers requests to a test's host with what its handler returns, and
/// records them.
private final class StubProtocol: URLProtocol {
    struct Recorded: Sendable {
        let path: String
        let body: String
    }

    typealias Handler = @Sendable (URLRequest) -> (Int, String)

    private static let routes = Mutex<[String: (handler: Handler, requests: [Recorded])]>([:])

    static let session: URLSession = {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [StubProtocol.self]
        return URLSession(configuration: configuration)
    }()

    /// Serves `host`; returns what it has received so far.
    static func serve(_ host: String, _ handler: @escaping Handler) -> @Sendable () -> [Recorded] {
        routes.withLock { $0[host] = (handler, []) }
        return { routes.withLock { $0[host]?.requests ?? [] } }
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let host = request.url?.host() ?? ""
        let body = request.httpBody ?? request.httpBodyStream.map(Self.read) ?? Data()
        let handler = Self.routes.withLock { routes -> Handler? in
            routes[host]?.requests.append(Recorded(
                path: request.url?.path() ?? "", body: String(decoding: body, as: UTF8.self)))
            return routes[host]?.handler
        }
        let (status, text) = handler?(request) ?? (404, "")
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1", headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(text.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}

    private static func read(_ stream: InputStream) -> Data {
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 4096)
        while stream.hasBytesAvailable {
            let count = stream.read(&buffer, maxLength: buffer.count)
            guard count > 0 else { break }
            data.append(buffer, count: count)
        }
        return data
    }
}
