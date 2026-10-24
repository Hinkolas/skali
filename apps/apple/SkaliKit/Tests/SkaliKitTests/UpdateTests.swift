import Foundation
import Testing
@testable import SkaliKit

struct UpdateTests {
    @Test func decodesAnUnmanagedDevelopmentDaemon() throws {
        // Shaped like what `task dev` answers.
        let json = """
        {"summary":{"state":"unknown","action":"","detail":"updates from the Studio need a coordinator-managed cluster",
          "progress":{"done":0,"total":0,"percent":0,"phase":"Preparing"}},
         "installed":{"version":"v0.0.0-dev"},"channel":"stable","auto_update":false,
         "last_checked_at":"2026-10-23T21:23:47.969643+02:00","latest":null,"update_available":false,
         "managed":false,"manageable":false,"nodes":[],"operation":null,"last_successful":null}
        """
        let status = try JSONCoding.decoder.decode(UpdateStatus.self, from: Data(json.utf8))
        #expect(status.summary.state == .unknown)
        #expect(status.summary.action == .none)
        #expect(status.operation == nil)
        #expect(!status.canAct)
        #expect(status.clusterVersion == "v0.0.0-dev")
    }

    @Test func decodesARunningOperation() throws {
        let json = """
        {"summary":{"state":"updating","target_version":"v0.2.0","action":"",
          "progress":{"done":2,"total":5,"percent":40,"phase":"Upgrading nodes"}},
         "installed":{"version":"v0.1.3"},"channel":"beta","auto_update":true,"last_checked_at":null,
         "latest":{"version":"v0.2.0","prerelease":false,"published_at":"2026-10-20T10:00:00Z","k3s":"v1.36.5+k3s1"},
         "update_available":true,"managed":true,"manageable":false,"reason":"an update is running",
         "nodes":[{"id":"n1","name":"cp-1","role":"server","k3s_version":"v1.36.5+k3s1","agent_version":"v0.2.0","phase":"active"},
                  {"id":"n2","name":"w-1","role":"agent","k3s_version":"v1.36.3+k3s1","agent_version":"v0.1.3","phase":"active"}],
         "operation":{"id":"op","phase":"upgrading-nodes","target_version":"v0.2.0","from_version":"v0.1.3",
           "started_at":"2026-10-23T10:00:00Z","updated_at":"2026-10-23T10:01:00Z",
           "steps":[{"node_id":"n1","node":"cp-1","action":"upgrade","phase":"complete"},
                    {"node_id":"n2","node":"w-1","action":"upgrade","phase":"running"}]},
         "last_successful":null}
        """
        let status = try JSONCoding.decoder.decode(UpdateStatus.self, from: Data(json.utf8))
        let operation = try #require(status.operation)
        #expect(operation.stage == .nodes)
        #expect(operation.completedSteps == 1)
        #expect(operation.runningSteps.map(\.node) == ["w-1"])
        #expect(operation.phaseLabel == "Upgrading nodes")
        #expect(status.latest?.k3s == "v1.36.5+k3s1")
    }

    @Test func olderDaemonsWithoutASummaryStillDecode() throws {
        let json = """
        {"installed":{"version":"v0.1.0"},"channel":"stable","auto_update":false,"last_checked_at":null,
         "latest":null,"update_available":false,"managed":true,"manageable":true,"nodes":[],"operation":null}
        """
        let status = try JSONCoding.decoder.decode(UpdateStatus.self, from: Data(json.utf8))
        #expect(status.summary.state == .unknown)
    }

    @Test func stagesFollowTheCoordinatorPhase() {
        func stage(_ phase: String) -> UpdateStage? {
            UpdateOperation(id: "x", phase: phase, startedAt: .now, updatedAt: .now).stage
        }
        #expect(stage("pending") == .nodes)
        #expect(stage("upgrading-nodes") == .nodes)
        #expect(stage("reconciling-platform") == .platform)
        #expect(stage("verifying") == .verify)
        #expect(stage("complete") == nil)
    }

    @Test func talliesVersionsAcrossNodes() {
        #expect(UpdateStatus.tally(["v1", "v1"]) == "v1")
        #expect(UpdateStatus.tally(["v1", "v2", "v1", nil]) == "2 on v1 · 1 on v2 · 1 not reported")
        #expect(UpdateStatus.tally([nil]) == "not reported")
        #expect(UpdateStatus.commonVersion(["v2", "v1", "v1"]) == "v1")
    }

    @Test func theDemoUpdateWalksEveryStage() async throws {
        let demo = DemoCluster(update: .available)
        var status = try await demo.updateStatus()
        #expect(status.summary.state == .available)
        #expect(status.canAct)
        status = try await demo.applyUpdate(version: "v0.1.3-rc.9")
        #expect(status.summary.state == .updating)
        #expect(status.operation?.stage == .nodes)
    }
}
