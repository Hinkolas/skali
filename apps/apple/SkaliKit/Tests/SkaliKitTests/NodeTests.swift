import Foundation
import Testing
@testable import SkaliKit

struct NodeTests {
    @Test func decodesAnObservedNode() throws {
        let json = """
        {"nodes":[{"name":"skali-app-01","role":"agent","capabilities":["builder"],"arch":"arm64","os":"linux",
          "kubelet_version":"v1.36.3+k3s1","ready":true,"schedulable":false,"internal_ip":"10.42.1.2",
          "last_heartbeat":null,"memory_allocatable_bytes":8589934592}],
         "observation":{"state":"fresh","stale_since":null,"last_sync":"2026-10-23T10:00:00Z"}}
        """
        let node = try #require(JSONCoding.decoder.decode(NodesResponse.self, from: Data(json.utf8)).nodes.first)
        #expect(node.platform == "linux/arm64")
        #expect(!node.isController)
        #expect(!node.schedulable)
        #expect(node.lastHeartbeat == nil)
        #expect(node.capabilities == ["builder"])
    }

    @Test func usageTakesTheNewestSampledBucket() {
        let metrics = NodeMetrics(
            window: .hour, stepSeconds: 60, timestamps: [.now, .now, .now],
            nodes: [.init(name: "a", cpuAllocatableMillicores: 4000, memoryAllocatableBytes: 1000,
                          cpuMillicores: [100, 1000, nil], memoryBytes: [nil, 250, nil])])
        let storage = [NodeStorage(name: "a", sampledAt: .now, capacityBytes: 100, usedBytes: 34, availableBytes: 66)]
        let usage = try! #require(NodeUsage.byNode(metrics: metrics, storage: storage)["a"])
        #expect(usage.cpuFraction == 0.25)
        #expect(usage.memoryFraction == 0.25)
        #expect(usage.diskFraction == 0.34)
        #expect(NodeUsage.byNode(metrics: nil, storage: nil).isEmpty)
    }

    @Test func formatsLikeTheStudio() {
        #expect(formatBytes(512) == "512 B")
        #expect(formatBytes(8 * 1 << 30) == "8 GiB")
        #expect(formatBytes(7_600 * 1 << 20) == "7.4 GiB")
        #expect(formatBytes(80 * 1 << 30) == "80 GiB")
        #expect(formatCores(250) == "250m")
        #expect(formatCores(1500) == "1.5")
        #expect(formatCores(4000) == "4")
        #expect(formatCores(12_000) == "12")
    }
}
