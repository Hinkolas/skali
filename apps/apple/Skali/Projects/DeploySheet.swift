import SkaliKit
import SwiftUI

/// Promoting the environment on screen to another one, or redeploying it:
/// where the revision comes from and goes, what the server plans to
/// change, and the go. Both reuse artifacts, so nothing builds; once
/// started, the sheet follows the run.
struct DeploySheet: View {
    enum Kind {
        /// The environment's running revision moves to another one.
        case promote
        /// The environment's revision runs again with its current values.
        case redeploy
    }

    let kind: Kind
    /// The environment on screen: a promotion's source, a redeploy's target.
    let environment: ProjectEnvironment
    let model: ProjectModel

    @Environment(\.dismiss) private var dismiss
    @State private var targetID: String?
    @State private var startedRun: String?

    init(_ kind: Kind, environment: ProjectEnvironment, model: ProjectModel) {
        self.kind = kind
        self.environment = environment
        self.model = model
        // A promotion opens on where this environment went last time, else
        // the first that may take it.
        let pickable = environment.promotionTargets(among: model.environments ?? [])
            .filter { $0.promotionRefusal(from: environment) == nil }
        let last = pickable.first { $0.name == environment.lastPromotionTarget }
        _targetID = State(initialValue: (last ?? pickable.first)?.id)
    }

    var body: some View {
        NavigationStack {
            Group {
                if let startedRun {
                    RunDetailView(runID: startedRun, model: model)
                } else {
                    content
                        .navigationTitle(kind == .promote ? "Promote" : "Redeploy")
                        .navigationBarTitleDisplayMode(.inline)
                }
            }
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(role: startedRun == nil ? .cancel : .close) { dismiss() }
                }
            }
        }
    }

    @ViewBuilder private var content: some View {
        if model.status.map({ $0.activeRevision == nil }) == true {
            ContentUnavailableView(
                "Nothing Running in \(environment.name)",
                systemImage: kind == .promote ? "arrow.up.circle" : "arrow.triangle.2.circlepath",
                description: Text(kind == .promote
                                  ? "A promotion moves the revision running here. Deploy into \(environment.name) first."
                                  : "A redeploy runs the deployed revision again. Deploy into \(environment.name) first."))
        } else if kind == .redeploy {
            DeployPlanView(source: .redeploy, target: environment, model: model) { startedRun = $0 }
        } else if let target = model.environments?.first(where: { $0.id == targetID }) {
            DeployPlanView(
                source: .promotion(fromEnvironmentID: environment.id), target: target, model: model,
                targets: environment.promotionTargets(among: model.environments ?? []),
                onPickTarget: { targetID = $0.id }
            ) { startedRun = $0 }
        } else {
            ContentUnavailableView(
                "No Environment Takes It", systemImage: "arrow.up.circle",
                description: Text("Every other environment of the project is locked for you, needs deploy access, or accepts promotions from elsewhere only."))
        }
    }
}

/// One deployment's preview: the route card, the server's warnings and
/// plan, a typed confirmation when the plan destroys data, and the button
/// that starts it.
private struct DeployPlanView: View {
    let source: DeploySource
    let target: ProjectEnvironment
    let model: ProjectModel
    /// A promotion's other targets, for its menu.
    var targets: [ProjectEnvironment] = []
    var onPickTarget: (ProjectEnvironment) -> Void = { _ in }
    let onStarted: (String) -> Void

    @State private var plan: DeployPlan?
    @State private var planError: String?
    /// The target's status, read for its revision and any restore hold;
    /// nil while it loads, and when it could not be read.
    @State private var targetStatus: EnvironmentStatus?
    @State private var targetStatusLoaded = false
    @State private var typed = ""
    @State private var deploying = false
    @State private var error: String?

    var body: some View {
        List {
            Section {
                card
            } footer: {
                Text(explanation)
            }

            if let hold = targetStatus?.incompleteRestore {
                Section {
                    Label {
                        Text(hold.isRunning
                             ? "A restore is replacing \(target.name)'s data. Deploys wait until it completes."
                             : "A restore of \(target.name) did not complete. Deploys are refused until a restore completes or its data is accepted.")
                    } icon: {
                        Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                    }
                    .font(.subheadline)
                }
            }

            planSections
        }
        .safeAreaBar(edge: .bottom) { startButton }
        .task(id: target.id) { await load() }
        .alert("Could Not Start", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
            Button("OK") {}
        } message: {
            Text(error ?? "")
        }
    }

    private var isPromotion: Bool { source != .redeploy }

    private var sourceEnvironment: ProjectEnvironment? {
        guard case .promotion(let id) = source else { return nil }
        return model.environments?.first { $0.id == id }
    }

    // MARK: Card

    @ViewBuilder private var card: some View {
        if let from = sourceEnvironment {
            HStack(alignment: .center, spacing: 10) {
                side("From", revision: revisionLine(model.status, loaded: model.status != nil)) {
                    Text(from.name).font(.headline.monospaced())
                }
                Image(systemName: "arrow.right")
                    .font(.headline)
                    .foregroundStyle(.tint)
                    .accessibilityHidden(true)
                side("To", revision: revisionLine(targetStatus, loaded: targetStatusLoaded)) {
                    if targets.count > 1 {
                        targetMenu
                    } else {
                        Text(target.name).font(.headline.monospaced())
                    }
                }
            }
            .padding(.vertical, 4)
        } else {
            HStack(spacing: 14) {
                SettingsIcon(systemImage: "arrow.triangle.2.circlepath", tint: .serviceApplication)
                side("Redeploy", revision: revisionLine(model.status, loaded: model.status != nil)) {
                    Text(target.name).font(.headline.monospaced())
                }
            }
            .padding(.vertical, 4)
        }
    }

    /// One end of the route: a caption, the environment, the revision it runs.
    private func side(_ caption: String, revision: Text, @ViewBuilder name: () -> some View) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(caption.uppercased())
                .font(.caption2.monospaced().weight(.medium))
                .foregroundStyle(.secondary)
            name()
            revision
                .font(.caption.monospaced())
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
    }

    private func revisionLine(_ status: EnvironmentStatus?, loaded: Bool) -> Text {
        guard loaded else { return Text("loading") }
        guard let revision = status?.activeRevision else {
            return Text(status == nil ? "revision unknown" : "nothing running")
        }
        return Text(revision.shortChecksum)
    }

    /// The target, and the other environments it could be instead; the
    /// ones that refuse say why.
    private var targetMenu: some View {
        Menu {
            ForEach(targets) { environment in
                let reason = sourceEnvironment.flatMap { environment.promotionRefusal(from: $0) }
                Toggle(isOn: Binding { environment.id == target.id } set: { if $0 { onPickTarget(environment) } }) {
                    Text(environment.name)
                    Text(reason ?? note(environment))
                    Image(systemName: environment.symbolName)
                }
                .disabled(reason != nil)
            }
        } label: {
            HStack(spacing: 5) {
                Text(target.name)
                    .font(.headline.monospaced())
                Image(systemName: "chevron.up.chevron.down")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(.secondary)
            }
            .foregroundStyle(.primary)
        }
        .accessibilityLabel("Target environment")
        .accessibilityValue(target.name)
    }

    /// "last used · promote-only · healthy · active".
    private func note(_ environment: ProjectEnvironment) -> String {
        var parts: [String] = []
        if environment.name == sourceEnvironment?.lastPromotionTarget { parts.append("last used") }
        if environment.isProtected { parts.append("promote-only") }
        parts.append(environment.stateDetail)
        return parts.joined(separator: " · ")
    }

    private var explanation: String {
        if let from = sourceEnvironment {
            return "The revision running in \(from.name) moves to \(target.name) with its artifacts, so nothing is rebuilt; \(target.name) keeps its own values."
        }
        return "The revision deployed in \(target.name) runs again with the environment's current values, the way saved value changes roll out. Its artifacts are reused, so nothing is rebuilt."
    }

    // MARK: Plan

    @ViewBuilder private var planSections: some View {
        if let planError {
            Section {
                Label {
                    Text(planError)
                } icon: {
                    Image(systemName: "xmark.octagon.fill").foregroundStyle(.red)
                }
                Button("Try Again") { Task { await load() } }
            }
        } else if let plan {
            if let warnings = plan.warnings, !warnings.isEmpty {
                Section("Warnings") {
                    ForEach(warnings, id: \.self) { warning in
                        Label {
                            Text(warning.message)
                        } icon: {
                            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                        }
                        .font(.subheadline)
                    }
                }
            }
            if plan.upToDate {
                Section {
                    Label {
                        Text(isPromotion
                             ? "There is nothing to promote: \(target.name) already runs this revision."
                             : "There is nothing to apply: everything is up to date.")
                    } icon: {
                        Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                    }
                }
            } else {
                ChangesSection(title: "Plan", changes: plan.changes, values: plan.values, planned: true)
                if plan.isDestructive {
                    Section {
                        Label {
                            Text("This plan removes or destroys stored data in \(target.name). It cannot be undone.")
                        } icon: {
                            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.red)
                        }
                        .font(.subheadline)
                        TextField(target.name, text: $typed)
                            .monospaced()
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                    } header: {
                        Text("Type \(target.name) to confirm")
                    }
                }
            }
        } else {
            Section {
                HStack(spacing: 10) {
                    ProgressView()
                    Text(isPromotion ? "Planning the promotion…" : "Planning the redeploy…")
                        .foregroundStyle(.secondary)
                }
            }
        }
    }

    private var startButton: some View {
        Button(action: start) {
            Group {
                if deploying {
                    ProgressView()
                } else {
                    Text(isPromotion ? "Promote to \(target.name)" : "Redeploy \(target.name)")
                        .lineLimit(1)
                }
            }
            .frame(maxWidth: .infinity)
        }
        .buttonStyle(.glassProminent)
        .controlSize(.large)
        .tint(plan?.isDestructive == true ? .red : nil)
        .disabled(!ready)
        .padding(.horizontal, 20)
        .padding(.bottom, 8)
    }

    private var ready: Bool {
        guard let plan, !plan.upToDate, planError == nil, !deploying else { return false }
        if targetStatus?.incompleteRestore != nil { return false }
        return !plan.isDestructive || typed.trimmingCharacters(in: .whitespaces) == target.name
    }

    // MARK: Actions

    private func load() async {
        plan = nil
        planError = nil
        typed = ""
        if isPromotion {
            targetStatus = nil
            targetStatusLoaded = false
        }
        async let status: Void = loadTargetStatus()
        do {
            plan = try await model.plan(source, into: target)
        } catch is CancellationError {
        } catch {
            planError = error.localizedDescription
        }
        await status
    }

    /// The revision line beside the target, and whether a restore holds it.
    private func loadTargetStatus() async {
        targetStatus = try? await model.status(of: target)
        targetStatusLoaded = true
    }

    private func start() {
        guard let plan else { return }
        deploying = true
        Task {
            defer { deploying = false }
            do {
                if let runID = try await model.deploy(source, into: target, allowDestructive: plan.isDestructive) {
                    onStarted(runID)
                } else {
                    // Something else brought it up to date since the plan.
                    await load()
                }
            } catch is CancellationError {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}
