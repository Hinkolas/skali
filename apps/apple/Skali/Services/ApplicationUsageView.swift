import SkaliKit
import SwiftUI

/// One application's usage over a window: CPU and memory against its
/// limits, and for a routed one the requests and traffic at the edge.
struct ApplicationUsageView: View {
    let key: String
    let spec: ApplicationSpec
    let model: ProjectModel

    @State private var window: NodeMetrics.Window = .day
    @State private var metrics: EnvironmentMetrics?
    @State private var failed = false

    var body: some View {
        List {
            Section {
                MetricWindowPicker(window: $window)
            }
            .listRowBackground(Color.clear)
            .listRowInsets(EdgeInsets(top: 0, leading: 0, bottom: 0, trailing: 0))

            if let series = metrics?.applications.first {
                let timestamps = metrics?.timestamps ?? []
                chartSection("CPU", note: limitNote(spec.resources?.limits?.milliCpu.map { "\(formatCores($0)) per pod" }),
                             series: [MetricSeries(label: "CPU", color: .usageCPU, points: points(timestamps, series.cpuMillicores.map { $0.map(Double.init) }))],
                             format: { formatCores(Int($0.rounded())) })
                chartSection("Memory", note: limitNote(spec.resources?.limits?.memoryBytes.map { "\(formatBytes($0)) per pod" }),
                             series: [MetricSeries(label: "Memory", color: .usageMemory, points: points(timestamps, series.memoryBytes.map { $0.map(Double.init) }))],
                             format: { formatBytes(Int64($0)) })
                if let edge = series.edge, let step = metrics?.stepSeconds {
                    let perMinute = 60 / Double(max(1, step))
                    chartSection("Requests", note: "per minute, through the edge",
                                 series: [MetricSeries(label: "Requests", color: .accentColor, points: points(timestamps, edge.requests.map { $0.map { Double($0) * perMinute } }))],
                                 format: { formatCount($0) })
                    chartSection("Traffic", note: "per \(stepLabel(step)), in and out",
                                 series: [
                                    MetricSeries(label: "In", color: .green, points: points(timestamps, edge.requestBytes.map { $0.map(Double.init) })),
                                    MetricSeries(label: "Out", color: .usageMemory, points: points(timestamps, edge.responseBytes.map { $0.map(Double.init) })),
                                 ],
                                 format: { formatBytes(Int64($0)) })
                }
            } else if failed {
                ContentUnavailableView("Usage Unavailable", systemImage: "chart.xyaxis.line")
            } else if metrics != nil {
                ContentUnavailableView("No Samples Yet", systemImage: "chart.xyaxis.line",
                                       description: Text("\(key) has not reported usage in this window."))
            }
        }
        .overlay {
            if metrics == nil && !failed { ProgressView() }
        }
        .navigationTitle("Usage")
        .navigationBarTitleDisplayMode(.large)
        .largeSubtitle("\(key) · \(model.environment?.name ?? "")", monospaced: true)
        .task(id: TaskKey(window: window, environment: model.environment?.id)) { await load() }
    }

    private struct TaskKey: Hashable {
        let window: NodeMetrics.Window
        let environment: String?
    }

    private func load() async {
        guard let id = model.environment?.id else { return }
        do {
            metrics = try await model.api.environmentMetrics(environmentID: id, window: window).only(key)
            failed = false
        } catch is CancellationError {
        } catch {
            failed = true
        }
    }

    private func chartSection(
        _ title: String, note: String?, series: [MetricSeries], format: @escaping (Double) -> String
    ) -> some View {
        Section {
            MetricChart(series: series, window: window, format: format)
                .frame(height: 170)
                .padding(.vertical, 6)
        } header: {
            Text(title)
        } footer: {
            let values = series.flatMap { $0.points.compactMap(\.value) }
            if let peak = values.max(), !values.isEmpty {
                let average = values.reduce(0, +) / Double(values.count)
                Text(["average \(format(average))", "peak \(format(peak))", note].compactMap(\.self).joined(separator: " · "))
                    .font(.caption.monospaced())
            }
        }
    }

    private func points(_ timestamps: [Date], _ values: [Double?]) -> [(date: Date, value: Double?)] {
        zip(timestamps, values).map { ($0, $1) }
    }

    private func limitNote(_ limit: String?) -> String? {
        limit.map { "limit \($0)" }
    }

    private func stepLabel(_ seconds: Int) -> String {
        seconds % 3600 == 0 ? "\(seconds / 3600) h" : "\(seconds / 60) min"
    }
}
