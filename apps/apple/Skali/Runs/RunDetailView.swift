import SkaliKit
import SwiftUI
import UIKit

/// One run: what it is, who started it and when, what it changed, and its
/// steps as they progress. A run still going follows its stream; tapping
/// a step opens its log.
struct RunDetailView: View {
    let runID: String
    let model: ProjectModel
    /// The sheet's first screen gets a close button.
    var closable = false

    @Environment(\.dismiss) private var dismiss
    @State private var tree: RunTree?
    @State private var missing = false
    @State private var copied = false
    @State private var confirmingCancel = false
    @State private var error: String?

    var body: some View {
        List {
            if let tree {
                Section {
                    header(tree)
                }
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets(top: 0, leading: 4, bottom: 4, trailing: 4))
                if let summary = tree.run.changeSummary, let deployment = tree.run.deployment {
                    Section {
                        ChangesDisclosure(summary: summary, deployment: deployment)
                    }
                }
                stepsSection(tree)
                if tree.run.isCancellable {
                    Section {
                        Button("Cancel \(tree.run.kindLabel.capitalized)", role: .destructive) { confirmingCancel = true }
                            .frame(maxWidth: .infinity)
                            .confirmationDialog(
                                "Cancel this \(tree.run.kindLabel)?", isPresented: $confirmingCancel, titleVisibility: .visible
                            ) {
                                Button("Cancel Run", role: .destructive) { cancel(tree.run) }
                                Button("Keep Running", role: .cancel) {}
                            } message: {
                                Text("Before promotion the run stops cleanly; after promotion the target is reverted.")
                            }
                    }
                }
            }
        }
        .overlay {
            if missing {
                ContentUnavailableView(
                    "Run Not Found", systemImage: "questionmark.circle",
                    description: Text("It may have been pruned from the history."))
            } else if tree == nil {
                ProgressView()
            }
        }
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            if closable {
                ToolbarItem(placement: .topBarTrailing) {
                    Button(role: .close) { dismiss() }
                }
            }
        }
        .navigationDestination(for: RunStep.self) { StepLogView(step: $0, api: model.api) }
        .task(id: runID) { await follow() }
        .alert("Could Not Cancel", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
            Button("OK") {}
        } message: {
            Text(error ?? "")
        }
    }

    // MARK: Header

    private func header(_ tree: RunTree) -> some View {
        let run = tree.run
        return VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .center, spacing: 10) {
                Text(run.kindLabel)
                    .font(.largeTitle.bold())
                RunStatusCapsule(status: run.status)
            }
            HStack(spacing: 6) {
                Text(run.shortID)
                Button {
                    UIPasteboard.general.string = run.id
                    copied = true
                } label: {
                    Image(systemName: copied ? "checkmark" : "doc.on.doc")
                        .font(.caption.weight(.medium))
                        .frame(width: 26, height: 22)
                        .background(.fill.tertiary, in: .rect(cornerRadius: 6))
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Copy Run ID")
                .sensoryFeedback(.success, trigger: copied) { _, new in new }
                .task(id: copied) {
                    guard copied else { return }
                    try? await Task.sleep(for: .seconds(1.5))
                    copied = false
                }
                Text("·")
                Text(actor(run))
                    .lineLimit(1)
            }
            .font(.footnote.monospaced())
            .foregroundStyle(.secondary)
            TimelineView(.periodic(from: .now, by: run.isActive ? 1 : 3600)) { context in
                Text(timing(run, now: context.date))
                    .font(.footnote.monospaced())
                    .foregroundStyle(.secondary)
            }
            if run.status == "failed", let failure = run.failure {
                Text(failure)
                    .font(.subheadline)
                    .foregroundStyle(.red)
            }
            if run.status == "succeeded", tree.deferredRoutes > 0 {
                Label(
                    tree.deferredRoutes == 1
                        ? "Succeeded with a warning: a route's domain does not point here yet, so its certificate waits."
                        : "Succeeded with warnings: \(tree.deferredRoutes) routes' domains do not point here yet, so their certificates wait.",
                    systemImage: "exclamationmark.triangle")
                    .font(.subheadline)
                    .foregroundStyle(.orange)
            }
            if run.isActive {
                ProgressView(value: tree.progress)
                    .tint(.orange)
                    .padding(.top, 4)
            }
        }
        .padding(.bottom, 8)
    }

    private func actor(_ run: Run) -> String {
        let name = describeActor(run.actor, directory: model.directory)
        return run.actor == model.userID ? "\(name) (you)" : name
    }

    private func timing(_ run: Run, now: Date) -> String {
        var parts = [run.createdAt.formatted(.dateTime.month(.abbreviated).day().year().hour().minute())]
        if let duration = run.duration(now: now) { parts.append(duration) }
        if let revision = run.deployment?.revision {
            parts.append("rev \(revision.replacingOccurrences(of: "sha256:", with: "").prefix(8))")
        }
        return parts.joined(separator: " · ")
    }

    // MARK: Steps

    @ViewBuilder private func stepsSection(_ tree: RunTree) -> some View {
        Section("Steps") {
            if tree.steps.isEmpty {
                if tree.run.isActive {
                    Label("Waiting for the reconciler to pick this up", systemImage: "hourglass")
                        .foregroundStyle(.secondary)
                } else {
                    Text("No steps were recorded.")
                        .foregroundStyle(.secondary)
                }
            } else {
                ForEach(tree.steps.flattened(), id: \.step.id) { step, depth in
                    NavigationLink(value: step) {
                        StepRow(step: step, depth: depth)
                    }
                }
            }
        }
    }

    // MARK: Following

    /// Reads the run, then follows its stream while it is unsettled; the
    /// stream ends once the run has, and a dropped one reconnects.
    private func follow() async {
        do {
            tree = try await model.api.run(id: runID)
        } catch let error as APIError where error.status == 404 {
            missing = true
            return
        } catch {}
        var delay = Duration.seconds(1)
        while !Task.isCancelled, tree?.run.isActive ?? true {
            do {
                for try await document in model.api.runUpdates(id: runID) {
                    tree = document
                    delay = .seconds(1)
                }
            } catch let error as APIError where error.status == 404 {
                missing = tree == nil
                return
            } catch {}
            guard tree?.run.isActive ?? true else { return }
            try? await Task.sleep(for: delay)
            delay = min(delay * 2, .seconds(15))
        }
    }

    private func cancel(_ run: Run) {
        Task {
            do {
                try await model.cancelRun(run)
            } catch is CancellationError {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

/// What a rollout changed: its one-line summary, opening to every service,
/// value and restart.
private struct ChangesDisclosure: View {
    let summary: String
    let deployment: RunDeployment

    var body: some View {
        if rowCount > 0 {
            DisclosureGroup {
                ForEach(deployment.changes ?? [], id: \.service) { change in
                    ChangeRow(name: serviceName(change.service), symbol: symbol(change.service),
                              action: change.action, detail: change.detail, destructive: change.destructive == true)
                }
                ForEach(Array((deployment.values ?? []).enumerated()), id: \.offset) { _, value in
                    ChangeRow(name: value.name, symbol: "key", action: value.action,
                              detail: value.services.map { "used by \($0.map(serviceName).joined(separator: ", "))" },
                              destructive: false)
                }
                ForEach(deployment.restarts ?? [], id: \.self) { service in
                    ChangeRow(name: serviceName(service), symbol: symbol(service), action: "restart", detail: nil,
                              destructive: false)
                }
            } label: {
                label
            }
        } else {
            label
        }
    }

    private var label: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text("Changes")
            Text(summary)
                .font(.subheadline)
                .foregroundStyle(.secondary)
        }
    }

    private var rowCount: Int {
        (deployment.changes?.count ?? 0) + (deployment.values?.count ?? 0) + (deployment.restarts?.count ?? 0)
    }

    private func symbol(_ service: String) -> String {
        if service.hasPrefix("databases.") { return ServiceKind.database.systemImage }
        if service.hasPrefix("buckets.") { return ServiceKind.bucket.systemImage }
        return ServiceKind.application.systemImage
    }
}

private struct ChangeRow: View {
    let name: String
    let symbol: String
    let action: String
    let detail: String?
    let destructive: Bool

    var body: some View {
        LabeledContent {
            Text(actionWord(action))
                .foregroundStyle(tint)
        } label: {
            Label {
                Text(name).monospaced()
                if let detail {
                    Text(detail)
                }
            } icon: {
                Image(systemName: symbol)
            }
        }
    }

    private var tint: Color {
        if destructive || action == "remove" || action == "prune" { return .red }
        if action == "create" { return .green }
        return .secondary
    }
}
