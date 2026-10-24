import SkaliKit
import SwiftUI

/// The pages a project's overview leads to.
enum ProjectRoute: Hashable {
    case service(ServiceKind, String)
    case deployments, backups, settings
    /// One run of the environment, by id.
    case run(String)
}

/// A project's overview for one environment: how it is doing, what it
/// uses and stores, its services, and the way to its deployments, backups
/// and settings. The environment is picked in the navigation bar.
struct ProjectDetailView: View {
    let project: Project

    @Environment(AppModel.self) private var app
    @Environment(ClusterSession.self) private var session

    var body: some View {
        ProjectOverviewScreen(model: app.projectModel(for: project, in: session))
            .id(project.id)
    }
}

private struct ProjectOverviewScreen: View {
    let model: ProjectModel
    @Environment(\.openURL) private var openURL
    @State private var showingStorage = false
    @State private var confirmingRestart = false
    @State private var confirmingStop = false
    @State private var deletingEnvironment = false
    @State private var deploying: DeploySheet.Kind?
    @State private var accepting: IncompleteRestore?
    @State private var startedRun: String?
    @State private var failure: ActionFailure?

    var body: some View {
        EnvironmentGate(model: model) { overview($0) }
            .projectPage(
                model, title: model.project.label, subtitle: model.summary, dot: model.health?.tint, followsStatus: true)
            .toolbar { toolbar }
            .navigationDestination(for: ProjectRoute.self) { route in
                switch route {
                case .deployments: DeploymentsView(model: model)
                case .backups: ProjectBackupsView(model: model)
                case .run(let id): RunDetailView(runID: id, model: model)
                case .service(let kind, let key): ServicePage(kind: kind, key: key, model: model)
                default: ProjectRoutePlaceholder(route: route)
                }
            }
            .sheet(isPresented: $showingStorage) {
                if let environment = model.environment {
                    NavigationStack {
                        StorageBreakdownView(
                            rollup: StorageRollup(storage: model.storage ?? [], environmentID: environment.id),
                            environment: environment.name)
                    }
                    .presentationDetents([.medium, .large])
                }
            }
            .sheet(item: $deploying) { kind in
                if let environment = model.environment {
                    DeploySheet(kind, environment: environment, model: model)
                }
            }
            .sheet(item: $accepting) { restore in
                if let environment = model.environment {
                    AcceptRestoreSheet(restore: restore, environment: environment, model: model)
                }
            }
            .sheet(isPresented: $deletingEnvironment) {
                if let environment = model.environment {
                    DeleteEnvironmentSheet(environment: environment, model: model)
                }
            }
            .navigationDestination(item: $startedRun) { RunDetailView(runID: $0, model: model) }
            .alert(failure?.title ?? "", isPresented: Binding { failure != nil } set: { if !$0 { failure = nil } }) {
                Button("OK") {}
            } message: {
                Text(failure?.message ?? "")
            }
            .onChange(of: model.status == nil, initial: true) { openInitialSheet() }
    }

    // MARK: Overview

    private func overview(_ environment: ProjectEnvironment) -> some View {
        List {
            if let restore = model.status?.incompleteRestore {
                RestoreHoldSection(restore: restore, environment: environment) { accepting = restore }
            }
            if model.definition?.applications?.isEmpty == false {
                Section {
                    UsageCarousel(stats: usageStats)
                        .redacted(reason: model.metrics == nil ? .placeholder : [])
                        .listRowInsets(EdgeInsets())
                        .listRowBackground(Color.clear)
                } header: {
                    Text("Last 24 Hours")
                }
            }
            storageSection(environment)
            servicesSection
            pagesSection(environment)
        }
    }

    private var usageStats: [UsageStat] {
        guard let metrics = model.metrics else { return UsageStat.Kind.allCases.map(UsageStat.empty) }
        return metrics.usageStats(limits: UsageLimits(definition: model.definition, status: model.status))
    }

    @ViewBuilder private func storageSection(_ environment: ProjectEnvironment) -> some View {
        let rollup = StorageRollup(storage: model.storage ?? [], environmentID: environment.id)
        if !rollup.services.isEmpty {
            Section("Storage") {
                Button {
                    showingStorage = true
                } label: {
                    HStack(spacing: 12) {
                        StorageCard(rollup: rollup)
                        Image(systemName: "chevron.right")
                            .font(.footnote.weight(.semibold))
                            .foregroundStyle(.tertiary)
                    }
                }
                .tint(.primary)
                .accessibilityHint("Shows the storage of every service")
            }
        }
    }

    @ViewBuilder private var servicesSection: some View {
        Section("Services") {
            if let definition = model.definition, definition.serviceCount > 0 {
                ForEach(definition.serviceItems(status: model.status)) { item in
                    NavigationLink(value: ProjectRoute.service(item.kind, item.key)) {
                        ProjectServiceRow(item: item)
                    }
                }
            } else {
                Text("No services yet. Define them in skali.yaml and run skali deploy.")
                    .foregroundStyle(.secondary)
            }
        }
    }

    private func pagesSection(_ environment: ProjectEnvironment) -> some View {
        Section {
            NavigationLink(value: ProjectRoute.deployments) {
                LabeledContent {
                    deploymentsValue
                } label: {
                    Label { Text("Deployments") } icon: {
                        SettingsIcon(systemImage: "paperplane", tint: .serviceApplication)
                    }
                }
            }
            NavigationLink(value: ProjectRoute.backups) {
                LabeledContent {
                    backupsValue(environment)
                } label: {
                    Label { Text("Backups") } icon: { SettingsIcon(systemImage: "archivebox", tint: .orange) }
                }
            }
            NavigationLink(value: ProjectRoute.settings) {
                Label { Text("Settings") } icon: { SettingsIcon(systemImage: "slider.horizontal.3") }
            }
        }
    }

    /// The rollout under way, else the last one: its outcome and age.
    @ViewBuilder private var deploymentsValue: some View {
        if let runs = model.runs {
            if let active = runs.first(where: { $0.isRollout && $0.isActive }) {
                RunStateLabel(text: active.activeLabel, tint: .orange)
            } else if let last = runs.latestRollout {
                RunStateLabel(
                    text: "\(compactAge(since: last.finishedAt ?? last.createdAt)) ago",
                    tint: last.status == "succeeded" ? .green : last.status == "failed" ? .red : .secondary)
            } else {
                Text("none yet")
            }
        }
    }

    /// The schedule, or a warning when data goes without one.
    @ViewBuilder private func backupsValue(_ environment: ProjectEnvironment) -> some View {
        if let schedule = environment.settings?.backup {
            Text(schedule.cadence)
        } else if model.definition?.hasStatefulServices == true {
            TagChip(text: "not backed up", tint: .orange)
        }
    }

    // MARK: Toolbar

    @ToolbarContentBuilder private var toolbar: some ToolbarContent {
        ToolbarItem(placement: .primaryAction) {
            Menu {
                Section {
                    Button { deploying = .promote } label: {
                        Text("Promote…")
                        if let promoteRefusal { Text(promoteRefusal) }
                        Image(systemName: "arrow.up.circle")
                    }
                    .disabled(!mayDeploy || promoteRefusal != nil)
                    Button { deploying = .redeploy } label: {
                        Text("Redeploy…")
                        if let redeployRefusal { Text(redeployRefusal) }
                        Image(systemName: "arrow.triangle.2.circlepath")
                    }
                    .disabled(!mayDeploy || redeployRefusal != nil)
                }
                Section {
                    Button("Restart Applications", systemImage: "arrow.clockwise") { confirmingRestart = true }
                        .disabled(!canRestart)
                }
                Section {
                    Button("Open in Studio", systemImage: "safari") { openURL(model.studioURL()) }
                }
                if let environment = model.environment {
                    Section {
                        Button { confirmingStop = true } label: {
                            Text("Stop Environment…")
                            if let reason = stopRefusal(environment) { Text(reason) }
                            Image(systemName: "stop.circle")
                        }
                        .disabled(!mayDeploy || stopRefusal(environment) != nil)
                        Button(role: .destructive) { deletingEnvironment = true } label: {
                            Text("Delete Environment…")
                            if let reason = deleteRefusal(environment) { Text(reason) }
                            Image(systemName: "trash")
                        }
                        .disabled(!mayDeploy || deleteRefusal(environment) != nil)
                    }
                }
            } label: {
                Label("More", systemImage: "ellipsis")
            }
            .confirmationDialog(
                "Restart every application in \(model.environment?.name ?? "")?", isPresented: $confirmingRestart,
                titleVisibility: .visible
            ) {
                Button("Restart Applications") { restart() }
            } message: {
                Text("Recreates their pods with the same revision, values and images. Databases, buckets and stored data are untouched.")
            }
            .confirmationDialog(
                "Stop \(model.environment?.name ?? "")?", isPresented: $confirmingStop, titleVisibility: .visible
            ) {
                Button("Stop Environment", role: .destructive) { stop() }
            } message: {
                Text("Takes the running workloads down. Values, revisions and volumes stay, and the next deploy starts the environment again.")
            }
        }
    }

    /// Nothing deploys into an environment, or a project, being deleted.
    private var mayDeploy: Bool {
        model.environmentIsReadable && model.project.deletingAt == nil
    }

    private var promoteRefusal: String? {
        guard let environment = model.environment, let environments = model.environments else { return nil }
        return promotionRefusal(source: environment, environments: environments, status: model.status)
    }

    private var redeployRefusal: String? {
        guard let environment = model.environment else { return nil }
        return SkaliKit.redeployRefusal(environment, status: model.status, runs: model.runs)
    }

    /// The sheet a debug launch asked for, once the environment's status
    /// is in.
    private func openInitialSheet() {
        #if DEBUG
        guard !Self.openedInitialSheet, let sheet = AppTab.initialProjectSheet, let status = model.status else { return }
        Self.openedInitialSheet = true
        switch sheet {
        case "promote": deploying = .promote
        case "redeploy": deploying = .redeploy
        case "accept": accepting = status.incompleteRestore
        case "delete": deletingEnvironment = true
        default: break
        }
        #endif
    }

    private static var openedInitialSheet = false

    private var canRestart: Bool {
        guard model.environmentIsReadable, let environment = model.environment else { return false }
        return environment.role >= .deploy && environment.state == "active"
            && model.definition?.applications?.isEmpty == false
    }

    private func restart() {
        Task {
            do {
                startedRun = try await model.restartApplications()
            } catch is CancellationError {
            } catch {
                failure = ActionFailure(title: "Could Not Restart", message: error.localizedDescription)
            }
        }
    }

    private func stop() {
        Task {
            do {
                startedRun = try await model.stopEnvironment()
            } catch is CancellationError {
            } catch {
                failure = ActionFailure(title: "Could Not Stop", message: error.localizedDescription)
            }
        }
    }
}

/// An action that could not start, for the alert.
private struct ActionFailure {
    let title: String
    let message: String
}

/// A run's state beside its row: a dot and a short word.
private struct RunStateLabel: View {
    let text: String
    let tint: Color

    var body: some View {
        HStack(spacing: 6) {
            Circle().fill(tint).frame(width: 7, height: 7)
            Text(text)
        }
    }
}

/// Stands in for the project's pages until they are built.
private struct ProjectRoutePlaceholder: View {
    let route: ProjectRoute

    var body: some View {
        ContentUnavailableView(title, systemImage: systemImage, description: Text("This screen is not built yet."))
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
    }

    private var title: String {
        switch route {
        case .service(_, let key): key
        case .deployments: "Deployments"
        case .backups: "Backups"
        case .settings: "Settings"
        case .run: "Run"
        }
    }

    private var systemImage: String {
        switch route {
        case .service(let kind, _): kind.systemImage
        case .deployments: "paperplane"
        case .backups: "archivebox"
        case .settings: "slider.horizontal.3"
        case .run: "paperplane"
        }
    }
}

private extension Run {
    var activeLabel: String {
        switch kind {
        case "rollback": "rolling back"
        case "restart": "restarting"
        default: "deploying"
        }
    }
}

#Preview {
    PreviewHost {
        ProjectDetailView(project: DemoCluster.projects[1])
    }
}

extension DeploySheet.Kind: Identifiable {
    var id: Self { self }
}
