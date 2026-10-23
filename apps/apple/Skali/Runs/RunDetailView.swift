import SkaliKit
import SwiftUI
import UIKit

/// One run as a page: its status under the title, where it stands (live
/// while it runs, the failure once it failed), who started it and when,
/// what it changed, and its work, per service the way the CLI lists a
/// deploy or as the full step tree. Steps open to their logs.
struct RunDetailView: View {
    let runID: String
    let model: ProjectModel

    enum Layout: String {
        case services, steps
    }

    @Environment(\.openURL) private var openURL
    @AppStorage("run.layout") private var layout: Layout = .services
    @State private var tree: RunTree?
    @State private var missing = false
    @State private var confirmingCancel = false
    @State private var error: String?

    var body: some View {
        content
            .navigationTitle(tree.map { $0.run.kindLabel.prefix(1).uppercased() + $0.run.kindLabel.dropFirst() } ?? "Run")
            .navigationBarTitleDisplayMode(.large)
            .largeSubtitle(subtitle, dot: tree.map { RunStatusStyle.tint($0.run.status) }, monospaced: true)
            .toolbar { menu }
            .navigationDestination(for: RunStep.self) { StepLogView(step: $0, api: model.api) }
            .navigationDestination(for: ServiceProgressLink.self) { link in
                ServiceStepsView(progress: tree?.serviceProgress?.first { $0.id == link.id })
            }
            .task(id: runID) { await follow() }
            .alert("Could Not Cancel", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
                Button("OK") {}
            } message: {
                Text(error ?? "")
            }
    }

    /// "succeeded · 2d ago", "running · since 00:45"; the id until the run is
    /// read (an empty subtitle would leave the title collapsed).
    private var subtitle: String {
        guard let run = tree?.run else { return String(runID.prefix(8)) }
        // The progress card counts the time; the subtitle stays still.
        if run.isActive {
            return "\(run.status) · since \(run.createdAt.formatted(date: .omitted, time: .shortened))"
        }
        return "\(run.status) · \(compactAge(since: run.finishedAt ?? run.createdAt)) ago"
    }

    /// Always a list, so the large title has its scroll view from the
    /// first frame on; loading and a missing run lie over it.
    private var content: some View {
        List {
            if let tree {
                stateSection(tree)
                detailsSection(tree.run)
                if let deployment = tree.run.deployment {
                    ChangesSection(deployment: deployment)
                }
                workSection(tree)
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
    }

    // MARK: Where it stands

    @ViewBuilder private func stateSection(_ tree: RunTree) -> some View {
        let run = tree.run
        if run.isActive {
            Section {
                RunProgressCard(tree: tree)
                if run.isCancellable {
                    Button("Cancel \(run.kindLabel.capitalized)", role: .destructive) { confirmingCancel = true }
                }
            }
        } else if run.status == "failed" {
            Section {
                Label {
                    Text(run.failure ?? "The run failed.")
                } icon: {
                    Image(systemName: "xmark.octagon.fill")
                        .foregroundStyle(.red)
                }
                if let step = Self.failedStep(tree.steps) {
                    NavigationLink(value: step) {
                        LabeledContent("Failed At") {
                            Text(step.title).lineLimit(2)
                        }
                    }
                }
            }
        } else if run.status == "succeeded", tree.deferredRoutes > 0 {
            Section {
                Label(
                    tree.deferredRoutes == 1
                        ? "A route's domain does not point here yet, so its certificate waits."
                        : "\(tree.deferredRoutes) routes' domains do not point here yet, so their certificates wait.",
                    systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
            } footer: {
                Text("The run succeeded with warnings. Certificates are issued once DNS points at the cluster.")
            }
        } else if run.status == "cancelled" {
            Section {
                Label("The run was cancelled.", systemImage: "slash.circle")
                    .foregroundStyle(.secondary)
            }
        }
    }

    /// The deepest failed step: where the failure happened.
    private static func failedStep(_ steps: [RunStep]) -> RunStep? {
        for step in steps where step.status == "failed" {
            return failedStep(step.children ?? []) ?? step
        }
        return nil
    }

    // MARK: Details

    private func detailsSection(_ run: Run) -> some View {
        Section {
            LabeledContent("Started By") {
                Text(run.actor == model.userID
                     ? "\(describeActor(run.actor, directory: model.directory)) (you)"
                     : describeActor(run.actor, directory: model.directory))
            }
            LabeledContent("Started") {
                Text(run.createdAt, format: .dateTime.month(.abbreviated).day().year().hour().minute())
            }
            if let started = run.startedAt {
                LabeledContent("Duration") {
                    TimelineView(.periodic(from: .now, by: run.isActive ? 1 : 3600)) { context in
                        Text(formatDuration(from: started, to: run.finishedAt ?? context.date))
                            .monospacedDigit()
                    }
                }
            }
            if let source = run.deployment?.fromEnvironment {
                LabeledContent("Promoted From") { Text(source).monospaced() }
            }
            if let revision = run.deployment?.revision {
                LabeledContent("Revision") {
                    Text(revision.replacingOccurrences(of: "sha256:", with: "").prefix(12)).monospaced()
                }
            }
            if run.bypassProtection == true {
                LabeledContent("Protection") {
                    Text("Bypassed").foregroundStyle(.orange)
                }
            }
            CopyableRow(title: "Run ID", value: run.id, shown: run.shortID)
        }
    }

    // MARK: Work

    @ViewBuilder private func workSection(_ tree: RunTree) -> some View {
        let services = tree.serviceProgress
        Section {
            if tree.steps.isEmpty {
                Label(tree.run.isActive ? "Waiting for the reconciler to pick this up" : "No steps were recorded.",
                      systemImage: tree.run.isActive ? "hourglass" : "list.bullet")
                    .foregroundStyle(.secondary)
            } else if let services, layout == .services {
                ForEach(services) { progress in
                    NavigationLink(value: ServiceProgressLink(id: progress.id)) {
                        ServiceProgressRow(progress: progress)
                    }
                }
            } else {
                ForEach(tree.steps.flattened(), id: \.step.id) { step, depth in
                    NavigationLink(value: step) {
                        StepRow(step: step, depth: depth)
                    }
                }
            }
        } header: {
            if services != nil, !tree.steps.isEmpty {
                Picker("Show", selection: $layout) {
                    Text("Services").tag(Layout.services)
                    Text("Steps").tag(Layout.steps)
                }
                .pickerStyle(.segmented)
                .textCase(nil)
                .padding(.bottom, 6)
            } else {
                Text("Steps")
            }
        }
    }

    // MARK: Menu

    @ToolbarContentBuilder private var menu: some ToolbarContent {
        ToolbarItem(placement: .primaryAction) {
            Menu {
                Button("Copy Run ID", systemImage: "doc.on.doc") { UIPasteboard.general.string = runID }
                Button("Open in Studio", systemImage: "safari") {
                    var url = model.studioURL("deployments")
                    url.append(queryItems: [URLQueryItem(name: "run", value: runID)])
                    openURL(url)
                }
                if let run = tree?.run, run.isCancellable {
                    Section {
                        Button("Cancel \(run.kindLabel.capitalized)", systemImage: "xmark.circle", role: .destructive) {
                            confirmingCancel = true
                        }
                    }
                }
            } label: {
                Label("More", systemImage: "ellipsis")
            }
            .confirmationDialog(
                "Cancel this \(tree?.run.kindLabel ?? "run")?", isPresented: $confirmingCancel, titleVisibility: .visible
            ) {
                Button("Cancel Run", role: .destructive) { cancel() }
                Button("Keep Running", role: .cancel) {}
            } message: {
                Text("Before promotion the run stops cleanly; after promotion the target is reverted.")
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

    private func cancel() {
        guard let run = tree?.run else { return }
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

/// A run in flight: the step it is on, how far along it is, and for how
/// long it has been going.
private struct RunProgressCard: View {
    let tree: RunTree

    var body: some View {
        let counts = tree.leafCounts
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 10) {
                ProgressView()
                    .controlSize(.small)
                Text(tree.currentStep?.title ?? "Waiting for the reconciler")
                    .font(.headline)
                    .lineLimit(2)
            }
            ProgressView(value: tree.progress)
                .tint(.orange)
            HStack {
                Text("\(counts.done) of \(counts.total) steps")
                Spacer()
                if let started = tree.run.startedAt {
                    TimelineView(.periodic(from: .now, by: 1)) { context in
                        Text(formatDuration(from: started, to: context.date))
                    }
                }
            }
            .font(.caption.monospaced())
            .foregroundStyle(.secondary)
        }
        .padding(.vertical, 4)
        .accessibilityElement(children: .combine)
    }
}

/// A value with its first block shown and the whole one copied.
private struct CopyableRow: View {
    let title: String
    let value: String
    let shown: String

    @State private var copied = false

    var body: some View {
        LabeledContent(title) {
            Button {
                UIPasteboard.general.string = value
                copied = true
            } label: {
                HStack(spacing: 6) {
                    Text(shown).monospaced()
                    Image(systemName: copied ? "checkmark" : "doc.on.doc")
                        .font(.footnote)
                        .contentTransition(.symbolEffect(.replace))
                }
            }
            .tint(.secondary)
            .sensoryFeedback(.success, trigger: copied) { _, new in new }
            .task(id: copied) {
                guard copied else { return }
                try? await Task.sleep(for: .seconds(1.5))
                copied = false
            }
            .accessibilityLabel("Copy \(title)")
        }
    }
}

/// What a rollout changed: every service, value and restart.
private struct ChangesSection: View {
    let deployment: RunDeployment

    @State private var showsValues = false

    var body: some View {
        Section("Changes") {
            ForEach(deployment.changes ?? [], id: \.service) { change in
                ChangeRow(name: serviceName(change.service), symbol: symbol(change.service), tint: kind(change.service)?.tint,
                          action: change.action, detail: change.detail, destructive: change.destructive == true)
            }
            ForEach(deployment.restarts ?? [], id: \.self) { service in
                ChangeRow(name: serviceName(service), symbol: symbol(service), tint: kind(service)?.tint,
                          action: "restart", detail: nil, destructive: false)
            }
            let values = deployment.values ?? []
            if !values.isEmpty {
                let names = Set(values.map(\.name)).count
                DisclosureGroup(isExpanded: $showsValues) {
                    ForEach(Array(values.enumerated()), id: \.offset) { _, value in
                        ChangeRow(name: value.name, symbol: nil, tint: nil, action: value.action,
                                  detail: value.services.map { "used by \($0.map(serviceName).joined(separator: ", "))" },
                                  destructive: false)
                    }
                } label: {
                    Label(names == 1 ? "1 value changed" : "\(names) values changed", systemImage: "key")
                }
            }
            if (deployment.changes ?? []).isEmpty && (deployment.restarts ?? []).isEmpty && values.isEmpty {
                Text("No changes")
                    .foregroundStyle(.secondary)
            }
        }
    }

    private func kind(_ service: String) -> ServiceKind? {
        if service.hasPrefix("applications.") { return .application }
        if service.hasPrefix("databases.") { return .database }
        if service.hasPrefix("buckets.") { return .bucket }
        return nil
    }

    private func symbol(_ service: String) -> String {
        kind(service)?.systemImage ?? "square.dashed"
    }
}

private struct ChangeRow: View {
    let name: String
    let symbol: String?
    let tint: Color?
    let action: String
    let detail: String?
    let destructive: Bool

    var body: some View {
        LabeledContent {
            Text(actionWord(action))
                .foregroundStyle(actionTint)
        } label: {
            Label {
                Text(name).monospaced()
                if let detail {
                    Text(detail)
                }
            } icon: {
                if let symbol {
                    Image(systemName: symbol)
                        .foregroundStyle(tint ?? .secondary)
                }
            }
        }
    }

    private var actionTint: Color {
        if destructive || action == "remove" || action == "prune" { return .red }
        if action == "create" { return .green }
        return .secondary
    }
}

/// A row of the Services view: the row's state, its kind and name, a dot
/// per step of its work, the step it is on, and how long it took.
private struct ServiceProgressRow: View {
    let progress: ServiceProgress

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            StepStatusIcon(status: progress.status)
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 6) {
                    if let kind = progress.kind {
                        Image(systemName: kind.systemImage)
                            .font(.subheadline)
                            .foregroundStyle(kind.tint)
                    }
                    Text(progress.name)
                        .font(progress.service == nil ? .body : .headline)
                        .foregroundStyle(progress.service == nil ? .secondary : .primary)
                }
                StepDots(steps: progress.steps)
                if ["running", "waiting", "failed"].contains(progress.status) {
                    Text(progress.current.title)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
            }
            Spacer(minLength: 8)
            TimelineView(.periodic(from: .now, by: progress.finishedAt == nil ? 1 : 3600)) { context in
                Text(progress.duration(now: context.date) ?? "")
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 2)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(progress.name)
        .accessibilityValue("\(progress.status), \(progress.steps.count) steps, on \(progress.current.title)")
    }
}

/// A dot per step, colored by its status.
private struct StepDots: View {
    let steps: [RunStep]

    var body: some View {
        FlowLayout(horizontalSpacing: 4, verticalSpacing: 4) {
            ForEach(steps) { step in
                Circle()
                    .fill(color(step))
                    .frame(width: 7, height: 7)
            }
        }
    }

    private func color(_ step: RunStep) -> Color {
        if step.isDeferredTLS { return .orange }
        switch step.status {
        case "succeeded": return .green
        case "failed": return .red
        case "running", "waiting": return .orange
        case "skipped", "cancelled": return Color(.tertiaryLabel)
        default: return Color(.quaternaryLabel)
        }
    }
}

/// Opens one service's work in the run.
struct ServiceProgressLink: Hashable {
    let id: String
}

/// One service's steps in a run; each opens to its log.
private struct ServiceStepsView: View {
    let progress: ServiceProgress?

    var body: some View {
        if let progress {
            List {
                Section {
                    ForEach(progress.steps) { step in
                        NavigationLink(value: step) {
                            StepRow(step: step, depth: 0)
                        }
                    }
                } footer: {
                    if let service = progress.service {
                        Text(service).monospaced()
                    } else {
                        Text("The environment's own work: validating, preparing, pruning, verifying and activating.")
                    }
                }
            }
            .navigationTitle(progress.name)
            .navigationBarTitleDisplayMode(.large)
            .largeSubtitle(
                [progress.status, progress.finishedAt == nil ? nil : progress.duration()].compactMap(\.self)
                    .joined(separator: " · "),
                dot: RunStatusStyle.tint(progress.status), monospaced: true)
        } else {
            ContentUnavailableView("No Steps", systemImage: "list.bullet")
        }
    }
}
