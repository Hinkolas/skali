import SkaliKit
import SwiftUI

/// Every node of the cluster, controllers first, each with its current CPU
/// and memory use. Refreshes every 30 seconds while on screen.
struct NodesView: View {
    @Environment(ClusterSession.self) private var session

    var body: some View {
        let nodes = session.system.nodes ?? []
        List {
            section("Controllers", nodes.filter(\.isController))
            section("Workers", nodes.filter { !$0.isController })
        }
        .overlay {
            if session.system.nodes == nil {
                ProgressView()
            } else if nodes.isEmpty {
                ContentUnavailableView(
                    "No Nodes Observed", systemImage: "server.rack",
                    description: Text("The daemon runs API-only, or the cluster has not synced yet."))
            }
        }
        .navigationTitle("Nodes")
        .largeSubtitle(subtitle(nodes), dot: nodes.isEmpty ? nil : nodes.allSatisfy(\.ready) ? .green : .orange)
        .task {
            while !Task.isCancelled {
                await session.loadNodes()
                try? await Task.sleep(for: .seconds(30))
            }
        }
        .refreshable { await session.loadNodes() }
    }

    @ViewBuilder private func section(_ title: String, _ nodes: [ClusterNode]) -> some View {
        if !nodes.isEmpty {
            Section("\(title) · \(nodes.count)") {
                ForEach(nodes.sorted { $0.name.localizedStandardCompare($1.name) == .orderedAscending }) { node in
                    NavigationLink(value: SettingsRoute.node(node.name)) {
                        NodeRow(node: node, usage: session.nodeUsage[node.name])
                    }
                }
            }
        }
    }

    /// "11/11 online · k3s v1.36.3".
    private func subtitle(_ nodes: [ClusterNode]) -> String {
        guard !nodes.isEmpty else { return "" }
        var parts = ["\(nodes.count { $0.ready })/\(nodes.count) online"]
        if let kubelet = UpdateStatus.commonVersion(nodes.map(\.kubeletVersion)) {
            parts.append(kubernetesLabel(kubelet))
        }
        return parts.joined(separator: " · ")
    }
}

/// One node: tile, name, role and architecture, and two usage bars.
private struct NodeRow: View {
    let node: ClusterNode
    let usage: NodeUsage?

    @ScaledMetric(relativeTo: .headline) private var tile: CGFloat = 38

    var body: some View {
        HStack(spacing: 12) {
            NodeTile(ready: node.ready, size: tile)
            VStack(alignment: .leading, spacing: 3) {
                Text(node.name)
                    .font(.headline.monospaced())
                    .lineLimit(1)
                Text(detail)
                    .font(.subheadline)
                    .foregroundStyle(node.ready ? .secondary : Color.red)
                    .lineLimit(1)
            }
            Spacer(minLength: 8)
            if node.ready {
                RowUsageBars(bars: [
                    .init(label: "cpu", fraction: usage?.cpuFraction, color: .usageCPU),
                    .init(label: "mem", fraction: usage?.memoryFraction, color: .usageMemory),
                ])
            }
        }
        .padding(.vertical, 2)
        .accessibilityElement(children: .combine)
        .accessibilityValue(accessibilityUsage)
    }

    private var detail: String {
        if !node.ready { return "offline · \(node.summary)" }
        if !node.schedulable { return "cordoned · \(node.summary)" }
        return node.summary
    }

    private var accessibilityUsage: String {
        [("CPU", usage?.cpuFraction), ("memory", usage?.memoryFraction)]
            .compactMap { label, fraction in fraction.map { "\(label) \(Int(($0 * 100).rounded())) percent" } }
            .joined(separator: ", ")
    }
}

#Preview {
    PreviewHost { NodesView() }
}
