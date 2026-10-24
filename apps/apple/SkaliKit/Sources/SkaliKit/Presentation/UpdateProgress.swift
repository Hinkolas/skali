import Foundation

// How the update screens read the update document, kept with the Studio's
// (studio/src/lib/types/updates.ts and the System > Updates page) so the
// app and the browser tell the same story.

/// The three stages an update walks: every node, then the platform, then a
/// verification pass.
public enum UpdateStage: Int, CaseIterable, Sendable, Comparable {
    case nodes, platform, verify

    public static func < (a: UpdateStage, b: UpdateStage) -> Bool { a.rawValue < b.rawValue }
}

extension UpdateOperation {
    /// The stage the coordinator phase belongs to; nil once complete.
    public var stage: UpdateStage? {
        switch phase {
        case "reconciling-platform": .platform
        case "verifying": .verify
        case "complete": nil
        default: .nodes
        }
    }

    public var isSettled: Bool { phase == "complete" || phase == "failed" }

    public var completedSteps: Int { steps.count { $0.phase == .complete } }

    public var runningSteps: [Step] { steps.filter { $0.phase == .running } }

    public var failedStep: Step? { steps.first { $0.phase == .failed } }

    /// The operator-facing name of the coordinator phase.
    public var phaseLabel: String {
        Self.phaseLabels[phase] ?? phase
    }

    private static let phaseLabels: [String: String] = [
        "pending": "Preparing",
        "upgrading-nodes": "Upgrading nodes",
        "adding-nodes": "Adding nodes",
        "activating-topology": "Checking nodes",
        "awaiting-platform-initialization": "Waiting for initialization",
        "reconciling-platform": "Moving the platform",
        "removing-nodes": "Removing nodes",
        "rebalancing-workloads": "Rebalancing workloads",
        "verifying": "Verifying",
        "complete": "Complete",
        "failed": "Failed",
    ]
}

extension UpdateStatus {
    /// The release the whole cluster runs, else what the daemon runs.
    public var clusterVersion: String {
        summary.convergedVersion ?? installed.platformVersion ?? installed.version
    }

    /// How many nodes run the cluster's release on their host agent.
    public var nodesOnClusterVersion: Int {
        nodes.count { $0.agentVersion == clusterVersion }
    }

    /// The Kubernetes version most nodes run.
    public var commonKubernetesVersion: String? {
        UpdateStatus.commonVersion(nodes.map(\.k3sVersion))
    }

    /// Whether the latest release moves Kubernetes.
    public var latestChangesKubernetes: Bool {
        guard let pin = latest?.k3s, let current = commonKubernetesVersion else { return false }
        return pin != current
    }

    /// An update can start (or be finished, or retried) from here.
    public var canAct: Bool {
        managed && (manageable || summary.action == .retry) && summary.action != .none
    }

    /// The version most values report, or nil when none reported one.
    public static func commonVersion(_ values: [String?]) -> String? {
        var counts: [String: Int] = [:]
        for value in values.compactMap(\.self) { counts[value, default: 0] += 1 }
        return counts.max { a, b in a.value != b.value ? a.value < b.value : a.key > b.key }?.key
    }

    /// One line for a component across nodes: the version when they agree,
    /// "2 on vA · 1 on vB" when they do not, plus how many never reported.
    public static func tally(_ values: [String?]) -> String {
        var counts: [String: Int] = [:]
        var missing = 0
        for value in values {
            if let value { counts[value, default: 0] += 1 } else { missing += 1 }
        }
        if counts.isEmpty { return "not reported" }
        var parts = counts
            .sorted { a, b in a.value != b.value ? a.value > b.value : a.key < b.key }
            .map { counts.count == 1 ? $0.key : "\($0.value) on \($0.key)" }
        if missing > 0 { parts.append("\(missing) not reported") }
        return parts.joined(separator: " · ")
    }
}
