import SkaliKit
import SwiftUI

/// A build step's insights: the report the CLI uploads once the build
/// ended. Build output never streams into the step's log, so a running
/// build has nothing to show yet.
struct BuildInsightsView: View {
    let step: RunStep
    let api: any ClusterAPI

    @State private var insights: BuildInsights?
    @State private var loaded = false
    @State private var error: String?

    var body: some View {
        List {
            if let insights {
                Section {
                    LabeledContent("Outcome") {
                        Text(insights.outcome)
                            .foregroundStyle(insights.outcome == "succeeded" ? .green : .red)
                    }
                    LabeledContent("Duration",
                                   value: Duration.seconds(insights.duration),
                                   format: .units(allowed: [.minutes, .seconds], fractionalPart: .show(length: 1)))
                    LabeledContent("Platform") { Text(insights.platform).monospaced() }
                    LabeledContent("Cached", value: "\(insights.cachedSteps) of \(insights.steps.count) steps")
                    if let failure = insights.error, !failure.isEmpty {
                        Text(failure)
                            .font(.caption.monospaced())
                            .foregroundStyle(.red)
                            .typesetAsCode()
                            .textSelection(.enabled)
                    }
                } header: {
                    HStack(spacing: 8) {
                        StepStatusIcon(step: step)
                        Text(step.status)
                    }
                }
                let slowest = insights.slowestSteps()
                if !slowest.isEmpty {
                    Section("Slowest Steps") {
                        ForEach(Array(slowest.enumerated()), id: \.offset) { _, slow in
                            HStack(alignment: .firstTextBaseline, spacing: 10) {
                                Text("\(slow.seconds ?? 0, format: .number.precision(.fractionLength(1)))s")
                                    .font(.caption.monospacedDigit())
                                    .foregroundStyle(.secondary)
                                Text(slow.name)
                                    .font(.caption.monospaced())
                                    .lineLimit(2)
                            }
                        }
                    }
                }
                Section {
                    Text(insights.log)
                        .font(.caption2.monospaced())
                        .typesetAsCode()
                        .textSelection(.enabled)
                } header: {
                    Text("Build Output")
                } footer: {
                    Text(insights.logTruncated
                         ? "\(insights.logLines) lines; the start and end are kept."
                         : "\(insights.logLines) lines")
                }
            }
        }
        .overlay {
            if step.isActive {
                ContentUnavailableView("Build Running", systemImage: "hammer",
                                       description: Text("The build's insights arrive once it ends."))
            } else if let error {
                ContentUnavailableView("Insights Unavailable", systemImage: "exclamationmark.triangle",
                                       description: Text(error))
            } else if !loaded {
                ProgressView()
            } else if insights == nil {
                ContentUnavailableView("No Insights", systemImage: "hammer",
                                       description: Text("No insights available for this build."))
            }
        }
        .navigationTitle(step.title)
        .navigationBarTitleDisplayMode(.inline)
        .task(id: "\(step.id):\(step.status)") { await load() }
    }

    private func load() async {
        guard !step.isActive else { return }
        do {
            insights = try await api.buildInsights(stepID: step.id)
        } catch is CancellationError {
            return
        } catch {
            self.error = error.localizedDescription
        }
        loaded = true
    }
}
