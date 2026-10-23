import SkaliKit
import SwiftUI

/// The pages a project's overview leads to.
enum ProjectRoute: Hashable {
    case service(ServiceKind, String)
    case deployments, backups, settings
}

/// A project's overview for one environment: how it is doing, what it
/// uses and stores, its services, and the way to its deployments, backups
/// and settings. The environment is picked in the navigation bar.
struct ProjectDetailView: View {
    let project: Project

    @Environment(ClusterSession.self) private var session

    var body: some View {
        ProjectOverviewScreen(project: project, session: session)
            .id(project.id)
    }
}

private struct ProjectOverviewScreen: View {
    @State private var model: ProjectModel
    @Environment(ClusterSession.self) private var session
    @Environment(\.openURL) private var openURL
    @State private var showingStorage = false
    @State private var confirmingRestart = false
    @State private var actionError: String?

    init(project: Project, session: ClusterSession) {
        _model = State(initialValue: ProjectModel(project: project, session: session))
    }

    var body: some View {
        content
            .navigationTitle(model.project.label)
            // The bar's middle holds the environment, not the title.
            .largeSubtitle(model.summary, dot: model.health?.tint, inline: false)
            .toolbar { toolbar }
            .navigationDestination(for: ProjectRoute.self) { ProjectRoutePlaceholder(route: $0) }
            .task { await model.followChanges() }
            .task(id: model.environment?.id) {
                guard model.environmentIsReadable, let id = model.environment?.id else { return }
                async let usage: Void = model.loadEnvironment()
                await model.followStatus(of: id)
                await usage
            }
            .refreshable { await model.refresh() }
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
            .alert("Could Not Restart", isPresented: Binding { actionError != nil } set: { if !$0 { actionError = nil } }) {
                Button("OK") {}
            } message: {
                Text(actionError ?? "")
            }
    }

    // MARK: States

    @ViewBuilder private var content: some View {
        if let ended = model.ended {
            ContentUnavailableView(
                ended == .deleted ? "Project Deleted" : "No Access",
                systemImage: ended == .deleted ? "trash" : "lock",
                description: Text(ended == .deleted
                                  ? "\(model.project.label) no longer exists."
                                  : "You no longer have access to \(model.project.label)."))
        } else if model.project.deletingAt != nil {
            ContentUnavailableView {
                Label("Deleting Project", systemImage: "trash")
            } description: {
                Text("Its environments are being deleted; the project goes with the last of them.")
            } actions: {
                ProgressView()
            }
        } else if let environments = model.environments {
            if let environment = model.environment {
                if environment.isLocked {
                    ContentUnavailableView(
                        "Environment Locked", systemImage: "lock",
                        description: Text("You can see that \(environment.name) exists, but not what runs in it. A project admin can give you access."))
                } else if environment.state == "releasing" {
                    ContentUnavailableView {
                        Label("Deleting \(environment.name)", systemImage: "trash")
                    } description: {
                        Text("The environment and its data are being deleted.")
                    } actions: {
                        ProgressView()
                    }
                } else {
                    overview(environment)
                }
            } else if environments.isEmpty {
                ContentUnavailableView(
                    "No Environments", systemImage: "square.stack.3d.up.slash",
                    description: Text("Run skali deploy in the project's repository to create its first environment."))
            }
        } else if let error = model.loadError {
            ContentUnavailableView {
                Label("Project Unavailable", systemImage: "exclamationmark.triangle")
            } description: {
                Text(error)
            } actions: {
                Button("Try Again") { Task { await model.refresh() } }
            }
        } else {
            ProgressView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    // MARK: Overview

    private func overview(_ environment: ProjectEnvironment) -> some View {
        List {
            if let restore = model.status?.incompleteRestore {
                Section {
                    RestoreNotice(restore: restore, environment: environment.name)
                }
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
                RunStateLabel(text: active.activeLabel, tint: .blue)
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
        if let environments = model.environments, let environment = model.environment {
            ToolbarItem(placement: .principal) {
                EnvironmentPicker(environments: environments, selected: environment, health: model.health) {
                    model.environmentName = $0.name
                }
            }
        }
        ToolbarItem(placement: .primaryAction) {
            Menu {
                Button("Restart Applications", systemImage: "arrow.clockwise") { confirmingRestart = true }
                    .disabled(!canRestart)
                Button("Open in Studio", systemImage: "safari") { openURL(studioURL) }
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
        }
    }

    private var canRestart: Bool {
        guard model.environmentIsReadable, let environment = model.environment else { return false }
        return environment.role >= .deploy && environment.state == "active"
            && model.definition?.applications?.isEmpty == false
    }

    private var studioURL: URL {
        var url = session.account.address.studioURL.appending(path: "projects/\(model.project.name)")
        if let environment = model.environment {
            url.append(queryItems: [URLQueryItem(name: "env", value: environment.name)])
        }
        return url
    }

    private func restart() {
        Task {
            do {
                try await model.restartApplications()
            } catch is CancellationError {
            } catch {
                actionError = error.localizedDescription
            }
        }
    }
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

/// A restore that stopped the environment and did not complete.
private struct RestoreNotice: View {
    let restore: IncompleteRestore
    let environment: String

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Label(restore.status == "running" ? "Restore Running" : "Restore Incomplete",
                  systemImage: "exclamationmark.triangle.fill")
                .font(.headline)
                .foregroundStyle(.orange)
            Text(restore.status == "running"
                 ? "A restore holds \(environment) down until it completes."
                 : "A restore stopped \(environment) and did not complete, so its data may be partial. Deploys are refused until a restore completes or its data is accepted.")
                .font(.subheadline)
                .foregroundStyle(.secondary)
            if let error = restore.error {
                Text(error)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 4)
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
        }
    }

    private var systemImage: String {
        switch route {
        case .service(let kind, _): kind.systemImage
        case .deployments: "paperplane"
        case .backups: "archivebox"
        case .settings: "slider.horizontal.3"
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
