import SkaliKit
import SwiftUI

/// One node: its state, what it uses now, how that moved over time, and
/// what it runs.
struct NodeDetailView: View {
    let name: String

    @Environment(ClusterSession.self) private var session
    @State private var window: NodeMetrics.Window = .day
    @State private var history: NodeMetrics.Series?
    @State private var timestamps: [Date] = []

    var body: some View {
        List {
            if let node {
                Section {
                    ResourceHeader(
                        systemImage: "server.rack", tint: node.ready ? .green : .secondary, name: node.name,
                        status: node.statusCapsule,
                        detail: [node.roleLabel, node.platform].compactMap(\.self).joined(separator: " · "))
                }
                .listRowBackground(Color.clear)

                Section {
                    usageTiles(session.nodeUsage[name])
                }
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets())

                usageSection
                detailsSection(node)
            }
        }
        .overlay {
            if session.system.nodes == nil {
                ProgressView()
            } else if node == nil {
                ContentUnavailableView("Node Not Found", systemImage: "server.rack",
                                       description: Text("\(name) is no longer part of the cluster."))
            }
        }
        .navigationTitle(name)
        .navigationBarTitleDisplayMode(.inline)
        .task {
            while !Task.isCancelled {
                await session.loadNodes()
                try? await Task.sleep(for: .seconds(30))
            }
        }
        .task(id: window) { await loadHistory() }
        .refreshable {
            await session.loadNodes()
            await loadHistory()
        }
    }

    private var node: ClusterNode? {
        session.system.nodes?.first { $0.name == name }
    }

    private func usageTiles(_ usage: NodeUsage?) -> some View {
        UsageTileRow {
            UsageTile(
                title: "CPU", value: UsageTile.percent(usage?.cpuFraction), fraction: usage?.cpuFraction,
                color: .usageCPU, caption: usage?.cpuAllocatableMillicores.map(cpuCapacity) ?? "no data")
            UsageTile(
                title: "Memory", value: UsageTile.percent(usage?.memoryFraction), fraction: usage?.memoryFraction,
                color: .usageMemory, caption: usage?.memoryAllocatableBytes.map { "of \(formatBytes($0))" } ?? "no data")
            UsageTile(
                title: "Disk", value: UsageTile.percent(usage?.diskFraction), fraction: usage?.diskFraction,
                color: .usageDisk, caption: usage?.diskCapacityBytes.map { "of \(formatBytes($0))" } ?? "no data")
        }
    }

    // MARK: Usage over time

    private var usageSection: some View {
        Section("Usage") {
            MetricWindowPicker(window: $window)
                .listRowSeparator(.hidden)
            MetricChart(
                series: chartSeries, window: window, yDomain: 0...1,
                format: { "\(Int(($0 * 100).rounded()))%" })
            .frame(height: 180)
            .padding(.vertical, 4)
        }
    }

    /// CPU and memory as shares of what the node can allocate.
    private var chartSeries: [MetricSeries] {
        guard let history else { return [] }
        let cpuTotal = Double(max(1, history.cpuAllocatableMillicores))
        let memoryTotal = Double(max(1, history.memoryAllocatableBytes))
        return [
            MetricSeries(label: "CPU", color: .usageCPU, points: zip(timestamps, history.cpuMillicores).map {
                ($0, $1.map { Double($0) / cpuTotal })
            }),
            MetricSeries(label: "Memory", color: .usageMemory, points: zip(timestamps, history.memoryBytes).map {
                ($0, $1.map { Double($0) / memoryTotal })
            }),
        ]
    }

    private func loadHistory() async {
        guard let metrics = try? await session.api.nodeMetrics(window: window) else { return }
        timestamps = metrics.timestamps
        history = metrics.nodes.first { $0.name == name }
    }

    // MARK: Details

    private func detailsSection(_ node: ClusterNode) -> some View {
        Section("Node") {
            LabeledContent("Role") { Text(node.roleLabel).monospaced() }
            if !node.capabilities.isEmpty {
                LabeledContent("Capabilities") {
                    Text(node.capabilities.joined(separator: ", ")).monospaced()
                }
            }
            if let platform = node.platform {
                LabeledContent("Architecture") { Text(platform).monospaced() }
            }
            if let ip = node.internalIp {
                LabeledContent("Internal IP") { Text(ip).monospaced().textSelection(.enabled) }
            }
            if let ip = node.externalIp {
                LabeledContent("Public IP") { Text(ip).monospaced().textSelection(.enabled) }
            }
            LabeledContent("Host Agent") { Text(agentVersion ?? "not reported").monospaced() }
            if let kubelet = node.kubeletVersion {
                LabeledContent("Kubernetes") { Text(kubelet).monospaced() }
            }
            LabeledContent("Scheduling", value: node.schedulable ? "Schedulable" : "Cordoned")
            if let heartbeat = node.lastHeartbeat {
                LabeledContent("Last Heartbeat", value: heartbeat, format: .relative(presentation: .named))
            }
        }
    }

    /// The host agent's version, which the update document reports per node.
    private var agentVersion: String? {
        session.system.updates?.nodes.first { $0.name == name }?.agentVersion
    }
}

#Preview {
    PreviewHost { NodeDetailView(name: "skali-app-04") }
}
