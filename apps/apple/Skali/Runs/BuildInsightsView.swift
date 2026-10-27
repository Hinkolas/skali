import SkaliKit
import SwiftUI

/// A build step's insights: the report the CLI uploads once the build
/// ended. Build output never streams into the step's log, so a running
/// build has nothing to show yet. The CLI settles the step before its
/// report lands, so a report missing just after the build finished is
/// asked for again for a while.
struct BuildInsightsView: View {
    let runID: String
    let api: any ClusterAPI

    /// The step as last seen: followed through the run's own updates while
    /// the build runs, since the run screen stops following once this one
    /// is pushed.
    @State private var step: RunStep
    @State private var insights: BuildInsights?
    @State private var loaded = false
    @State private var error: String?

    init(step: RunStep, runID: String, api: any ClusterAPI) {
        self.runID = runID
        self.api = api
        _step = State(initialValue: step)
    }

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
        .task(id: step.id) { await follow() }
    }

    /// Follows a running build to its end, then loads its report.
    private func follow() async {
        if step.isActive {
            do {
                for try await tree in api.runUpdates(id: runID) {
                    if let latest = tree.step(id: step.id) { step = latest }
                    if !step.isActive { break }
                }
            } catch {
                if error is CancellationError { return }
            }
        }
        await load()
    }

    private func load() async {
        guard !step.isActive else { return }
        do {
            while true {
                insights = try await api.buildInsights(stepID: step.id)
                guard insights == nil, step.insightsMayArrive(at: .now) else { break }
                try await Task.sleep(for: .seconds(2))
            }
        } catch is CancellationError {
            return
        } catch {
            self.error = error.localizedDescription
        }
        loaded = true
    }
}
