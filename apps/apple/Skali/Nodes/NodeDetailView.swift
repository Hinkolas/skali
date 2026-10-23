import Charts
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
                    header(node)
                }
                .listRowBackground(Color.clear)

                Section {
                    UsageTiles(usage: session.nodeUsage[name])
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

    private func header(_ node: ClusterNode) -> some View {
        VStack(spacing: 10) {
            NodeTile(ready: node.ready, size: 72)
            Text(node.name)
                .font(.title2.monospaced().weight(.bold))
                .lineLimit(1)
                .minimumScaleFactor(0.7)
            HStack(spacing: 10) {
                StatusCapsule(node: node)
                Text(([node.roleLabel, node.platform]).compactMap(\.self).joined(separator: " · "))
                    .font(.subheadline.monospaced())
                    .foregroundStyle(.secondary)
            }
        }
        .frame(maxWidth: .infinity)
        .accessibilityElement(children: .combine)
    }

    // MARK: Usage over time

    private var usageSection: some View {
        Section {
            Picker("Window", selection: $window) {
                ForEach(NodeMetrics.Window.allCases) { window in
                    Text(window.label).tag(window)
                }
            }
            .pickerStyle(.segmented)
            .listRowSeparator(.hidden)

            UsageChart(series: history, timestamps: timestamps, window: window)
                .frame(height: 180)
                .padding(.vertical, 4)
        } header: {
            Text("Usage")
        }
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

/// "● Online" in green, or what keeps the node from being online.
private struct StatusCapsule: View {
    let node: ClusterNode

    var body: some View {
        let tint: Color = node.ready ? (node.schedulable ? .green : .orange) : .red
        Label {
            Text(node.ready ? (node.schedulable ? "Online" : "Cordoned") : "Offline")
        } icon: {
            Circle().fill(tint).frame(width: 7, height: 7)
        }
        .labelStyle(.titleAndIcon)
        .font(.subheadline.weight(.medium))
        .foregroundStyle(tint)
        .padding(.horizontal, 10)
        .padding(.vertical, 4)
        .background(tint.opacity(0.15), in: .capsule)
    }
}

/// CPU, memory and disk now: the share, a bar, and the capacity.
private struct UsageTiles: View {
    let usage: NodeUsage?

    var body: some View {
        HStack(spacing: 10) {
            UsageTile(
                title: "CPU", fraction: usage?.cpuFraction, color: .usageCPU,
                capacity: usage?.cpuAllocatableMillicores.map(cpuCapacity))
            UsageTile(
                title: "Memory", fraction: usage?.memoryFraction, color: .usageMemory,
                capacity: usage?.memoryAllocatableBytes.map { "of \(formatBytes($0))" })
            UsageTile(
                title: "Disk", fraction: usage?.diskFraction, color: .usageDisk,
                capacity: usage?.diskCapacityBytes.map { "of \(formatBytes($0))" })
        }
    }
}

/// "of 4 cores", "of 1 core", "of 500m".
private func cpuCapacity(_ millicores: Int) -> String {
    switch millicores {
    case ..<1000: "of \(formatCores(millicores))"
    case 1000: "of 1 core"
    default: "of \(formatCores(millicores)) cores"
    }
}

private struct UsageTile: View {
    let title: String
    let fraction: Double?
    let color: Color
    let capacity: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title.uppercased())
                .font(.caption2.monospaced().weight(.medium))
                .foregroundStyle(.secondary)
            Text(fraction.map { "\(Int(($0 * 100).rounded()))%" } ?? "—")
                .font(.title2.weight(.bold).monospacedDigit())
                .contentTransition(.numericText())
            UsageBar(fraction: fraction, color: color, height: 5)
            Text(capacity ?? "no data")
                .font(.caption2.monospaced())
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .minimumScaleFactor(0.8)
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(.secondarySystemGroupedBackground), in: .rect(cornerRadius: 20))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(title)
        .accessibilityValue([fraction.map { "\(Int(($0 * 100).rounded())) percent" }, capacity].compactMap(\.self).joined(separator: ", "))
    }
}

/// CPU and memory as shares of what the node can allocate, over the window.
private struct UsageChart: View {
    let series: NodeMetrics.Series?
    let timestamps: [Date]
    let window: NodeMetrics.Window

    private struct Point: Identifiable {
        let id: Int
        let date: Date
        let metric: String
        let value: Double
    }

    var body: some View {
        let points = self.points
        if points.isEmpty {
            ContentUnavailableView("No Samples Yet", systemImage: "chart.xyaxis.line")
        } else {
            Chart(points) { point in
                LineMark(
                    x: .value("Time", point.date),
                    y: .value("Usage", point.value))
                .foregroundStyle(by: .value("Metric", point.metric))
                .interpolationMethod(.monotone)
            }
            .chartForegroundStyleScale(["CPU": Color.usageCPU, "Memory": Color.usageMemory])
            .chartYScale(domain: 0...1)
            .chartYAxis {
                AxisMarks(values: [0, 0.5, 1]) { value in
                    AxisGridLine()
                    AxisValueLabel {
                        if let share = value.as(Double.self) { Text("\(Int(share * 100))%") }
                    }
                }
            }
            .chartXAxis {
                AxisMarks(values: .automatic(desiredCount: 4)) { _ in
                    AxisGridLine()
                    AxisValueLabel(format: window == .week ? .dateTime.weekday(.abbreviated) : .dateTime.hour().minute())
                }
            }
            .chartLegend(position: .top, alignment: .leading)
        }
    }

    private var points: [Point] {
        guard let series else { return [] }
        var points: [Point] = []
        let cpuTotal = Double(max(1, series.cpuAllocatableMillicores))
        let memoryTotal = Double(max(1, series.memoryAllocatableBytes))
        for (index, date) in timestamps.enumerated() {
            if index < series.cpuMillicores.count, let cpu = series.cpuMillicores[index] {
                points.append(Point(id: index * 2, date: date, metric: "CPU", value: Double(cpu) / cpuTotal))
            }
            if index < series.memoryBytes.count, let memory = series.memoryBytes[index] {
                points.append(Point(id: index * 2 + 1, date: date, metric: "Memory", value: Double(memory) / memoryTotal))
            }
        }
        return points
    }
}

extension NodeMetrics.Window {
    var label: String {
        switch self {
        case .hour: "1 Hour"
        case .day: "24 Hours"
        case .week: "7 Days"
        }
    }
}

#Preview {
    PreviewHost { NodeDetailView(name: "skali-app-04") }
}
