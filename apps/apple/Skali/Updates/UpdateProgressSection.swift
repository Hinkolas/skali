import SkaliKit
import SwiftUI

/// An update under way (or stopped): the overall progress, the three
/// stages as a vertical stepper, and what is happening right now.
struct UpdateProgressSection: View {
    let status: UpdateStatus
    /// Opens the nodes sheet, focused on a node.
    let showNode: (String?) -> Void

    var body: some View {
        let operation = status.operation
        let progress = status.summary.progress
        let failed = status.summary.state == .failed

        Section {
            VStack(alignment: .leading, spacing: 8) {
                HStack(alignment: .firstTextBaseline) {
                    Text(failed ? "Stopped" : progress.phase)
                        .font(.headline)
                    Spacer()
                    Text("\(progress.percent)%")
                        .font(.headline.monospacedDigit())
                        .foregroundStyle(.secondary)
                        .contentTransition(.numericText(value: Double(progress.percent)))
                }
                ProgressView(value: Double(progress.percent), total: 100)
                    .tint(failed ? .red : .accentColor)
                Text("\(progress.done) of \(progress.total) steps")
                    .font(.footnote.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
            .padding(.vertical, 6)
            .animation(.default, value: progress.percent)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(failed ? "Update stopped" : progress.phase)
            .accessibilityValue("\(progress.percent) percent, \(progress.done) of \(progress.total) steps")

            StageStepper(operation: operation, failed: failed)
                .padding(.vertical, 6)

            if failed {
                failure(operation)
            } else if let line = nowLine(operation) {
                HStack(alignment: .firstTextBaseline, spacing: 10) {
                    PulsingDot()
                    Text(line)
                        .font(.subheadline)
                }
                .padding(.vertical, 2)
            }
        } footer: {
            if !failed {
                Text("Running applications keep serving. The connection to the cluster can drop for a moment while the control plane restarts.")
            }
        }
    }

    @ViewBuilder private func failure(_ operation: UpdateOperation?) -> some View {
        let step = operation?.failedStep
        VStack(alignment: .leading, spacing: 6) {
            Label {
                if let step {
                    Text("\(Text(step.node).monospaced()) did not finish upgrading")
                } else {
                    Text("The update stopped")
                }
            } icon: {
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.red)
            }
            .font(.headline)
            Text(step?.error ?? operation?.error ?? "No error was recorded.")
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
        }
        .padding(.vertical, 4)
        Button("Show Node") { showNode(step?.nodeId) }
    }

    private func nowLine(_ operation: UpdateOperation?) -> String? {
        guard let operation else { return "Preparing the update." }
        switch operation.stage {
        case .nodes:
            let running = operation.runningSteps.map(\.node)
            if running.isEmpty { return "Preparing the next node." }
            return "Upgrading \(running.formatted()): the host agent and Kubernetes move to the new release."
        case .platform:
            return "Moving skalid and the Studio to the new release. The cluster may not answer for a moment."
        case .verify:
            return "Checking that every node reports the new release."
        case nil:
            return nil
        }
    }
}

/// Nodes, Platform, Verify: done, active, failed or still to come, joined
/// by a line like a delivery tracker.
private struct StageStepper: View {
    let operation: UpdateOperation?
    let failed: Bool

    @ScaledMetric(relativeTo: .body) private var marker: CGFloat = 28

    var body: some View {
        let current = operation?.stage ?? (operation?.phase == "complete" ? nil : .nodes)
        VStack(alignment: .leading, spacing: 0) {
            ForEach(UpdateStage.allCases, id: \.self) { stage in
                let state = state(of: stage, current: current)
                HStack(alignment: .top, spacing: 14) {
                    VStack(spacing: 0) {
                        StageMarker(state: state, number: stage.rawValue + 1, size: marker)
                        if stage != UpdateStage.allCases.last {
                            Rectangle()
                                .fill(state == .done ? Color.green.opacity(0.5) : Color.secondary.opacity(0.25))
                                .frame(width: 2)
                                .frame(minHeight: 14, maxHeight: .infinity)
                        }
                    }
                    VStack(alignment: .leading, spacing: 2) {
                        Text(stage.title)
                            .font(.body.weight(state == .upcoming ? .regular : .semibold))
                            .foregroundStyle(state == .upcoming ? .secondary : .primary)
                        Text(subtitle(stage))
                            .font(.subheadline)
                            .foregroundStyle(.secondary)
                    }
                    .padding(.top, (marker - 20) / 2)
                    .padding(.bottom, stage == UpdateStage.allCases.last ? 0 : 14)
                }
                .accessibilityElement(children: .combine)
                .accessibilityValue(state.accessibilityLabel)
            }
        }
    }

    private func state(of stage: UpdateStage, current: UpdateStage?) -> StageMarker.State {
        guard let current else { return .done }
        if stage < current { return .done }
        if stage == current { return failed ? .failed : .active }
        return .upcoming
    }

    private func subtitle(_ stage: UpdateStage) -> String {
        switch stage {
        case .nodes:
            let steps = operation?.steps ?? []
            return "\(operation?.completedSteps ?? 0) of \(steps.count) upgraded, controllers first"
        case .platform: return "skalid and the Studio"
        case .verify: return "Every node reports back"
        }
    }
}

private struct StageMarker: View {
    enum State {
        case done, active, failed, upcoming

        var accessibilityLabel: String {
            switch self {
            case .done: "Done"
            case .active: "In progress"
            case .failed: "Stopped"
            case .upcoming: "Not started"
            }
        }
    }

    let state: State
    let number: Int
    let size: CGFloat

    var body: some View {
        ZStack {
            Circle().fill(background)
            switch state {
            case .done:
                Image(systemName: "checkmark").font(.system(size: size * 0.42, weight: .bold)).foregroundStyle(.green)
            case .active:
                ProgressView().controlSize(.small).tint(.accentColor)
            case .failed:
                Image(systemName: "exclamationmark").font(.system(size: size * 0.46, weight: .bold)).foregroundStyle(.red)
            case .upcoming:
                Text("\(number)").font(.system(size: size * 0.44, weight: .semibold).monospacedDigit()).foregroundStyle(.secondary)
            }
        }
        .frame(width: size, height: size)
    }

    private var background: Color {
        switch state {
        case .done: .green.opacity(0.16)
        case .active: .accentColor.opacity(0.18)
        case .failed: .red.opacity(0.16)
        case .upcoming: Color.secondary.opacity(0.14)
        }
    }
}

/// The "happening now" dot.
private struct PulsingDot: View {
    @State private var pulsing = false

    var body: some View {
        Circle()
            .fill(.orange)
            .frame(width: 8, height: 8)
            .opacity(pulsing ? 0.35 : 1)
            .animation(.easeInOut(duration: 0.9).repeatForever(autoreverses: true), value: pulsing)
            .onAppear { pulsing = true }
            .accessibilityHidden(true)
    }
}

extension UpdateStage {
    var title: String {
        switch self {
        case .nodes: "Nodes"
        case .platform: "Platform"
        case .verify: "Verify"
        }
    }
}
