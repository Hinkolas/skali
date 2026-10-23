import Foundation
import Synchronization

/// The demo cluster's software update: eleven nodes, a newer release on
/// the beta channel, and an update that, once started, walks every node,
/// the platform and the verification over about a minute and a half.
public final class DemoUpdates: Sendable {
    public enum Scenario: String, Sendable {
        case current, available, updating, failed
    }

    private struct State {
        var scenario: Scenario
        var startedAt = Date()
        var lastChecked = Date.now.addingTimeInterval(-12 * 3600)
        var settings = UpdateSettings(channel: .beta, autoUpdate: false)
    }

    private let state: Mutex<State>

    init(scenario: Scenario) {
        state = Mutex(State(scenario: scenario))
    }

    private static let installed = "v0.1.3-rc.8"
    private static let target = "v0.1.3-rc.9"
    private static let k3s = "v1.36.3+k3s1"
    private static let targetK3s = "v1.36.5+k3s1"
    private static let nodeSeconds = 6.0
    private static let platformSeconds = 8.0
    private static let verifySeconds = 5.0

    private static let nodeNames: [(id: String, name: String, controller: Bool)] =
        (1...3).map { ("cp-\($0)", "cp-\($0)", true) } + (1...8).map { ("worker-\($0)", "worker-\($0)", false) }

    func save(_ settings: UpdateSettings) {
        state.withLock { $0.settings = settings }
    }

    func scan() {
        state.withLock { $0.lastChecked = Date() }
    }

    func start() {
        state.withLock {
            $0.scenario = .updating
            $0.startedAt = Date()
        }
    }

    func status() -> UpdateStatus {
        let state = state.withLock { $0 }
        let latest = Release(
            version: Self.target, prerelease: true, publishedAt: .now.addingTimeInterval(-2 * 86_400),
            url: "https://skali.dev/releases/\(Self.target)", k3s: Self.targetK3s)
        let finished = UpdateOperation(
            id: "op-earlier", phase: "complete", targetVersion: Self.installed, fromVersion: "v0.1.3-rc.7",
            startedAt: .now.addingTimeInterval(-12 * 86_400), updatedAt: .now.addingTimeInterval(-12 * 86_400),
            completedAt: .now.addingTimeInterval(-12 * 86_400))

        func status(
            summary: UpdateStatus.Summary, nodes: [UpdateNode], operation: UpdateOperation? = nil,
            available: Bool = true, lastSuccessful: UpdateOperation? = finished
        ) -> UpdateStatus {
            UpdateStatus(
                installed: .init(version: summary.convergedVersion ?? Self.installed, platformVersion: summary.convergedVersion),
                summary: summary, channel: state.settings.channel, autoUpdate: state.settings.autoUpdate,
                lastCheckedAt: state.lastChecked, latest: latest, updateAvailable: available,
                nodes: nodes, operation: operation, lastSuccessful: lastSuccessful)
        }

        switch state.scenario {
        case .current:
            return status(
                summary: .init(state: .current, convergedVersion: Self.installed),
                nodes: nodes(upgraded: 0), available: false)
        case .available:
            return status(
                summary: .init(state: .available, convergedVersion: Self.installed, targetVersion: Self.target, action: .update),
                nodes: nodes(upgraded: 0))
        case .failed:
            let steps = steps(done: 4, running: nil, failedAt: 4)
            let operation = UpdateOperation(
                id: "op-demo", phase: "failed", targetVersion: Self.target, fromVersion: Self.installed,
                startedAt: .now.addingTimeInterval(-25 * 60), updatedAt: .now.addingTimeInterval(-14 * 60),
                error: "worker-2 did not finish upgrading", steps: steps)
            return status(
                summary: .init(
                    state: .failed, convergedVersion: nil, targetVersion: Self.target, action: .retry,
                    progress: .init(done: 4, total: 13, percent: 31, phase: "Upgrading nodes")),
                nodes: nodes(upgraded: 4), operation: operation)
        case .updating:
            return updating(since: state.startedAt, status: status)
        }
    }

    private func updating(
        since start: Date,
        status: (UpdateStatus.Summary, [UpdateNode], UpdateOperation?, Bool, UpdateOperation?) -> UpdateStatus
    ) -> UpdateStatus {
        let elapsed = Date().timeIntervalSince(start)
        let nodeCount = Self.nodeNames.count
        let nodesTime = Double(nodeCount) * Self.nodeSeconds
        let done = min(nodeCount, Int(elapsed / Self.nodeSeconds))
        let total = nodeCount + 2

        let phase: String
        var completed = done
        switch elapsed {
        case ..<1: phase = "pending"
        case ..<nodesTime: phase = "upgrading-nodes"
        case ..<(nodesTime + Self.platformSeconds): phase = "reconciling-platform"
        case ..<(nodesTime + Self.platformSeconds + Self.verifySeconds):
            phase = "verifying"
            completed += 1
        default: phase = "complete"
        }

        if phase == "complete" {
            let operation = UpdateOperation(
                id: "op-demo", phase: "complete", targetVersion: Self.target, fromVersion: Self.installed,
                startedAt: start, updatedAt: Date(), completedAt: Date(),
                steps: steps(done: nodeCount, running: nil, failedAt: nil))
            return status(
                .init(state: .current, convergedVersion: Self.target), nodes(upgraded: nodeCount),
                operation, false, operation)
        }

        let operation = UpdateOperation(
            id: "op-demo", phase: phase, targetVersion: Self.target, fromVersion: Self.installed,
            startedAt: start, updatedAt: Date(),
            steps: steps(done: done, running: phase == "upgrading-nodes" ? done : nil, failedAt: nil))
        let progress = UpdateStatus.Summary.Progress(
            done: completed, total: total, percent: completed * 100 / total,
            phase: operation.phaseLabel)
        return status(
            .init(state: .updating, targetVersion: Self.target, progress: progress),
            nodes(upgraded: done), operation, true, nil)
    }

    private func nodes(upgraded: Int) -> [UpdateNode] {
        Self.nodeNames.enumerated().map { index, node in
            let new = index < upgraded
            return UpdateNode(
                id: node.id, name: node.name, role: node.controller ? "server" : "agent",
                k3sVersion: new ? Self.targetK3s : Self.k3s,
                agentVersion: new ? Self.target : Self.installed,
                coordinatorVersion: node.controller ? (new ? Self.target : Self.installed) : nil,
                coordinatorLastSeen: node.controller ? .now.addingTimeInterval(-8) : nil,
                phase: "active", lastSeen: .now.addingTimeInterval(-Double(5 + index)))
        }
    }

    private func steps(done: Int, running: Int?, failedAt: Int?) -> [UpdateOperation.Step] {
        Self.nodeNames.enumerated().map { index, node in
            let phase: UpdateOperation.Step.Phase =
                index < done ? .complete : index == running ? .running : index == failedAt ? .failed : .pending
            return UpdateOperation.Step(
                nodeId: node.id, node: node.name, phase: phase,
                error: index == failedAt
                    ? "drain timed out after 10m: pod skali-system/pg-shared-0 is protected by a PodDisruptionBudget that allows no evictions"
                    : nil)
        }
    }
}
