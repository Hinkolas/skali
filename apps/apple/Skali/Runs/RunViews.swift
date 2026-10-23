import SkaliKit
import SwiftUI

// How runs and their steps look wherever they are listed: the Deployments
// page, a backup's activity, a run's own sheet.

/// The Studio's run status colors: in progress amber, done green, failed
/// red, queued and cancelled quiet.
enum RunStatusStyle {
    static func tint(_ status: String) -> Color {
        switch status {
        case "running", "waiting": .orange
        case "succeeded": .green
        case "failed": .red
        default: .secondary
        }
    }

    static func label(_ status: String) -> String {
        status.prefix(1).uppercased() + status.dropFirst()
    }

    static func symbol(_ status: String) -> String {
        switch status {
        case "succeeded": "checkmark"
        case "failed": "xmark"
        case "running", "waiting": "arrow.triangle.2.circlepath"
        case "cancelled": "slash.circle"
        default: "clock"
        }
    }
}

/// The run's outcome as a glyph on its tinted tile.
struct RunStatusTile: View {
    let status: String

    @ScaledMetric(relativeTo: .body) private var side: CGFloat = 36

    var body: some View {
        let tint = RunStatusStyle.tint(status)
        Image(systemName: RunStatusStyle.symbol(status))
            .font(.system(size: side * 0.42, weight: .semibold))
            .foregroundStyle(tint)
            .symbolEffect(.rotate, isActive: status == "running")
            .frame(width: side, height: side)
            .background(tint.opacity(0.15), in: .rect(cornerRadius: side * 0.27))
            .overlay(RoundedRectangle(cornerRadius: side * 0.27).strokeBorder(tint.opacity(0.25)))
            .accessibilityHidden(true)
    }
}

/// One run in a list: kind and id, who started it, when and for how long,
/// and its status.
struct RunRow: View {
    let run: Run
    let directory: [String: DirectoryUser]

    var body: some View {
        HStack(spacing: 12) {
            RunStatusTile(status: run.status)
            VStack(alignment: .leading, spacing: 3) {
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    Text(run.kindLabel)
                        .font(.headline)
                    Text(run.shortID)
                        .font(.caption.monospaced())
                        .foregroundStyle(.secondary)
                }
                // A run in flight counts its time up.
                TimelineView(.periodic(from: .now, by: run.isActive ? 1 : 3600)) { context in
                    Text(meta(now: context.date))
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                }
            }
            Spacer(minLength: 8)
            Text(RunStatusStyle.label(run.status))
                .font(.subheadline)
                .foregroundStyle(RunStatusStyle.tint(run.status))
        }
        .padding(.vertical, 2)
        .accessibilityElement(children: .combine)
    }

    private func meta(now: Date) -> String {
        var parts = [describeActor(run.actor, directory: directory), "\(compactAge(since: run.createdAt, now: now)) ago"]
        if let duration = run.duration(now: now) { parts.append(duration) }
        return parts.joined(separator: " · ")
    }
}

/// "● succeeded": a run's status in its color, as the sheet's title shows it.
struct RunStatusCapsule: View {
    let status: String

    var body: some View {
        StatusCapsule(text: status, tint: RunStatusStyle.tint(status))
    }
}

/// The glyph for a step's status; a step in flight spins.
struct StepStatusIcon: View {
    let step: RunStep

    var body: some View {
        Group {
            if step.status == "running" {
                ProgressView().controlSize(.small)
            } else {
                Image(systemName: symbol)
                    .foregroundStyle(tint)
                    .font(.body.weight(.medium))
            }
        }
        .frame(width: 22)
        .accessibilityLabel(step.status)
    }

    private var symbol: String {
        if step.isDeferredTLS { return "exclamationmark.circle" }
        switch step.status {
        case "succeeded": return "checkmark.circle"
        case "failed": return "xmark.circle"
        case "skipped": return "minus.circle"
        case "cancelled": return "slash.circle"
        case "waiting": return "clock"
        default: return "circle.dashed"
        }
    }

    private var tint: Color {
        if step.isDeferredTLS { return .orange }
        switch step.status {
        case "succeeded": return .green
        case "failed": return .red
        case "waiting": return .orange
        case "pending": return Color(.tertiaryLabel)
        default: return .secondary
        }
    }
}

/// One step of a run's tree, indented by its depth, with its progress and
/// how long it took.
struct StepRow: View {
    let step: RunStep
    let depth: Int

    @ScaledMetric(relativeTo: .body) private var indent: CGFloat = 18

    var body: some View {
        HStack(spacing: 10) {
            StepStatusIcon(step: step)
            Text(step.title)
                .foregroundStyle(step.status == "pending" || step.status == "skipped" ? .secondary : .primary)
                .lineLimit(2)
            if let total = step.progressTotal, total > 0 {
                Text("\(step.progressCurrent ?? 0)/\(total)")
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
            }
            Spacer(minLength: 8)
            if let started = step.startedAt {
                TimelineView(.periodic(from: .now, by: step.isActive ? 1 : 3600)) { context in
                    Text(formatDuration(from: started, to: step.finishedAt ?? context.date))
                        .font(.caption.monospaced())
                        .foregroundStyle(.secondary)
                }
            }
        }
        .padding(.leading, CGFloat(depth) * indent)
        .accessibilityElement(children: .combine)
    }
}

extension [RunStep] {
    /// The tree in reading order with each step's depth.
    func flattened(depth: Int = 0) -> [(step: RunStep, depth: Int)] {
        flatMap { step in [(step, depth)] + (step.children ?? []).flattened(depth: depth + 1) }
    }
}

/// A run to show in a sheet.
struct RunSheetItem: Identifiable, Hashable {
    let id: String
}

/// A run in a sheet of its own, with a close button.
struct RunSheet: View {
    let runID: String
    let model: ProjectModel

    var body: some View {
        NavigationStack {
            RunDetailView(runID: runID, model: model, closable: true)
        }
    }
}
