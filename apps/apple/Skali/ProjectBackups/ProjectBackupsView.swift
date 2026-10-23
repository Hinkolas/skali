import SkaliKit
import SwiftUI

/// A project's backups: the environment's automatic schedule, the
/// snapshots on the backup target (this environment's or every one's),
/// and backing up now. A snapshot opens to restore or delete it; the menu
/// has the backup and restore runs and the schedule.
struct ProjectBackupsView: View {
    let model: ProjectModel

    @Environment(AppModel.self) private var app
    @State private var scope: SnapshotScope = .environment
    @State private var confirmingBackup = false
    @State private var shownRun: RunSheetItem?
    @State private var shownSnapshot: BackupSnapshot?
    @State private var showingSchedule = false
    @State private var showingActivity = false
    @State private var deleting: BackupSnapshot?
    @State private var error: (title: String, message: String)?

    enum SnapshotScope: Hashable {
        case environment, all
    }

    var body: some View {
        EnvironmentGate(model: model) { environment in
            content(environment)
        }
        .projectPage(
            model, title: "Backups", subtitle: "snapshots on the backup target", monospaced: true,
            also: { await model.loadSnapshots() })
        .toolbar { toolbar }
        .sheet(item: $shownRun) { RunSheet(runID: $0.id, model: model) }
        .sheet(item: $shownSnapshot) { SnapshotSheet(snapshot: $0, model: model) }
        .sheet(isPresented: $showingSchedule) {
            if let environment = model.environment {
                BackupScheduleSheet(environment: environment, model: model)
            }
        }
        .sheet(isPresented: $showingActivity) {
            if let environment = model.environment {
                BackupActivitySheet(environment: environment, model: model)
            }
        }
        .alert(
            "Delete this snapshot?",
            isPresented: Binding { deleting != nil } set: { if !$0 { deleting = nil } },
            presenting: deleting
        ) { snapshot in
            Button("Delete", role: .destructive) { delete(snapshot) }
        } message: { snapshot in
            Text("Taken from \(snapshot.environment) \(snapshot.createdAt.formatted(.relative(presentation: .named))) (\(snapshot.contents), \(formatBytes(snapshot.bytes))). Its data is removed from the backup target and cannot be restored afterwards.")
        }
        .alert(error?.title ?? "", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
            Button("OK") {}
        } message: {
            Text(error?.message ?? "")
        }
    }

    // MARK: Content

    private func content(_ environment: ProjectEnvironment) -> some View {
        List {
            Section("Schedule") {
                Button {
                    showingSchedule = true
                } label: {
                    ScheduleRow(schedule: environment.settings?.backup, stateful: stateful)
                }
                .tint(.primary)
            }
            Section {
                snapshotRows(environment)
            } header: {
                HStack {
                    Text("Snapshots")
                    Spacer()
                    scopeMenu(environment)
                }
            } footer: {
                if case .loaded = model.snapshots {
                    Text("Manual snapshots stay until deleted. Scheduled ones expire under the environment's retention.")
                }
            }
        }
    }

    private func scopeMenu(_ environment: ProjectEnvironment) -> some View {
        Menu {
            Picker("Snapshots", selection: $scope) {
                Text(environment.name).tag(SnapshotScope.environment)
                Text("All Environments").tag(SnapshotScope.all)
            }
        } label: {
            HStack(spacing: 4) {
                Text(scope == .environment ? environment.name : "All")
                Image(systemName: "chevron.down")
                    .font(.caption.weight(.semibold))
            }
            .font(.subheadline)
            .textCase(nil)
        }
        .accessibilityLabel("Show snapshots of")
    }

    @ViewBuilder private func snapshotRows(_ environment: ProjectEnvironment) -> some View {
        switch model.snapshots {
        case .unknown:
            ProgressView()
                .frame(maxWidth: .infinity)
        case .refused(let refusal):
            refusalView(refusal)
        case .loaded(let all):
            let shown = scope == .all ? all : all.filter { !$0.isOrphaned && $0.environment == environment.name }
            if shown.isEmpty {
                emptyView(environment)
            } else {
                ForEach(shown) { snapshot in
                    Button {
                        shownSnapshot = snapshot
                    } label: {
                        HStack(spacing: 10) {
                            SnapshotRow(snapshot: snapshot, showsEnvironment: scope == .all)
                            Image(systemName: "chevron.right")
                                .font(.footnote.weight(.semibold))
                                .foregroundStyle(.tertiary)
                        }
                    }
                    .tint(.primary)
                    .swipeActions {
                        if model.canDelete(snapshot) {
                            Button("Delete", role: .destructive) { deleting = snapshot }
                        }
                    }
                }
            }
        }
    }

    private func emptyView(_ environment: ProjectEnvironment) -> some View {
        ContentUnavailableView {
            Label(stateful ? "No Snapshots Yet" : "Nothing to Back Up", systemImage: "archivebox")
        } description: {
            if !stateful {
                Text("This project declares no database, bucket or volume, so there is nothing to snapshot.")
            } else if scope == .environment {
                Text("Choose Back Up Now in the menu to take one of \(environment.name), or turn on automatic backups.")
            } else {
                Text("Back up an environment now, or turn on automatic backups.")
            }
        }
    }

    @ViewBuilder private func refusalView(_ refusal: SnapshotsRefusal) -> some View {
        if refusal.isUnconfigured {
            ContentUnavailableView {
                Label("No Backup Target", systemImage: "icloud.slash")
            } description: {
                Text(model.isInstanceAdmin
                     ? "Snapshots need an S3 bucket outside the cluster. Set one up in Settings."
                     : "Snapshots need an S3 bucket outside the cluster, which an instance admin sets up.")
            } actions: {
                if model.isInstanceAdmin {
                    Button("Set Up Backup Target") {
                        app.selectedTab = .settings
                        app.settingsPath = [.backups]
                    }
                }
            }
        } else if refusal.isUnsupported {
            ContentUnavailableView(
                "Backups Unavailable", systemImage: "icloud.slash",
                description: Text("This cluster does not serve backups. A daemon in API-only mode runs without them."))
        } else if refusal.status == 403 {
            ContentUnavailableView(
                "Snapshots Not Visible", systemImage: "lock",
                description: Text("Read access to the project is required to list its snapshots."))
        } else {
            ContentUnavailableView(
                refusal.isUnreachable ? "Backup Target Not Answering" : "Snapshots Unavailable",
                systemImage: "icloud.slash", description: Text(refusal.message))
        }
    }

    // MARK: Toolbar

    /// Backing up lives in the menu, so the environment keeps the middle
    /// of the bar.
    @ToolbarContentBuilder private var toolbar: some ToolbarContent {
        ToolbarItem(placement: .primaryAction) {
            Menu {
                Section {
                    Button {
                        confirmingBackup = true
                    } label: {
                        Label("Back Up Now", systemImage: "arrow.up.doc")
                        if let backupRefusal { Text(backupRefusal) }
                    }
                    .disabled(backupRefusal != nil)
                }
                Section {
                    Button {
                        showingActivity = true
                    } label: {
                        Label("Activity", systemImage: "clock.arrow.circlepath")
                        Text("Backup and restore runs")
                    }
                    Button {
                        showingSchedule = true
                    } label: {
                        Label("Schedule Settings", systemImage: "clock")
                    }
                }
            } label: {
                Label("More", systemImage: "ellipsis")
            }
            .disabled(!model.environmentIsReadable)
            .confirmationDialog(
                "Back up \(model.environment?.name ?? "") now?", isPresented: $confirmingBackup,
                titleVisibility: .visible
            ) {
                Button("Back Up") { backUp() }
            } message: {
                Text("Every database, bucket and volume is snapshotted to the backup target. Manual snapshots are kept until you delete them.")
            }
        }
    }

    private var stateful: Bool {
        model.definition?.hasStatefulServices ?? false
    }

    /// Why Back Up is off, or nil when it may run. The daemon has the last
    /// word; this only covers what the page already knows.
    private var backupRefusal: String? {
        guard model.environmentIsReadable, let environment = model.environment else { return "no environment" }
        if environment.role < .deploy { return "deploy access required" }
        if environment.state != "active" { return "the environment is not running" }
        if !stateful { return "nothing to back up" }
        if case .refused = model.snapshots { return "no backup target" }
        return nil
    }

    // MARK: Actions

    private func backUp() {
        Task {
            do {
                shownRun = RunSheetItem(id: try await model.startBackup())
            } catch is CancellationError {
            } catch {
                self.error = ("Could Not Back Up", error.localizedDescription)
            }
        }
    }

    private func delete(_ snapshot: BackupSnapshot) {
        Task {
            do {
                try await model.deleteSnapshot(snapshot)
            } catch is CancellationError {
            } catch {
                self.error = ("Could Not Delete", error.localizedDescription)
            }
        }
    }
}

/// The environment's automatic backups at a glance.
private struct ScheduleRow: View {
    let schedule: BackupSchedule?
    let stateful: Bool

    @ScaledMetric(relativeTo: .body) private var tile: CGFloat = 36

    var body: some View {
        HStack(spacing: 12) {
            ResourceTile(systemImage: "clock", tint: schedule == nil ? .secondary : .green, size: tile)
            VStack(alignment: .leading, spacing: 2) {
                Text("Automatic Backups")
                    .font(.headline)
                Group {
                    if let schedule {
                        Text(scheduleDescription(schedule))
                        if !stateful {
                            Text("Nothing to back up yet: no database, bucket or volume.")
                                .foregroundStyle(.orange)
                        } else if let next = ScheduleDraft(schedule: schedule).nextRun(after: .now) {
                            Text("keeps \(describeRetention(seconds: schedule.retentionSeconds)) · next \(next, format: .relative(presentation: .named))")
                        } else {
                            Text("keeps \(describeRetention(seconds: schedule.retentionSeconds))")
                        }
                    } else {
                        Text("Only manual snapshots; nothing expires.")
                    }
                }
                .font(.subheadline)
                .foregroundStyle(.secondary)
            }
            Spacer(minLength: 8)
            Text(schedule == nil ? "Off" : "On")
                .foregroundStyle(.secondary)
            Image(systemName: "chevron.right")
                .font(.footnote.weight(.semibold))
                .foregroundStyle(.tertiary)
        }
        .padding(.vertical, 2)
    }
}

/// "Daily at 03:00 UTC": a schedule in words, with the time zone it runs in.
func scheduleDescription(_ schedule: BackupSchedule) -> String {
    let words = describeCron(schedule.schedule)
    guard words != schedule.schedule else { return schedule.schedule }
    let sentence = words.prefix(1).uppercased() + words.dropFirst()
    return words.contains(":") && !words.hasPrefix("hourly") ? "\(sentence) UTC" : sentence
}

/// One snapshot: when it was taken, how, what it holds and its size.
struct SnapshotRow: View {
    let snapshot: BackupSnapshot
    var showsEnvironment = false

    @ViewBuilder private var environmentChip: some View {
        if snapshot.isOrphaned {
            TagChip(text: "\(snapshot.environment), deleted", tint: .orange)
        } else if showsEnvironment {
            TagChip(text: snapshot.environment)
        }
    }

    private var details: some View {
        Text("\(snapshot.isScheduled ? "scheduled" : "manual") · \(snapshot.contents)")
            .font(.subheadline)
            .foregroundStyle(.secondary)
    }

    @ScaledMetric(relativeTo: .body) private var tile: CGFloat = 36

    var body: some View {
        HStack(spacing: 12) {
            ResourceTile(
                systemImage: snapshot.isScheduled ? "calendar.badge.clock" : "hand.tap",
                tint: snapshot.isScheduled ? .orange : .serviceApplication, size: tile)
            VStack(alignment: .leading, spacing: 3) {
                Text(snapshot.createdAt, format: .dateTime.month(.abbreviated).day().hour().minute())
                    .font(.headline)
                    .lineLimit(1)
                // The environment beside the details when both fit on
                // one line, above them when not.
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: 6) {
                        environmentChip
                        details.lineLimit(1)
                    }
                    VStack(alignment: .leading, spacing: 3) {
                        environmentChip
                        details.lineLimit(2)
                    }
                }
            }
            Spacer(minLength: 8)
            VStack(alignment: .trailing, spacing: 3) {
                Text(formatBytes(snapshot.bytes))
                    .font(.subheadline.monospacedDigit())
                Text("\(compactAge(since: snapshot.createdAt)) ago")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 2)
        .accessibilityElement(children: .combine)
    }
}

extension ProjectModel {
    /// Delete needs maintain on the environment the snapshot came from,
    /// or project admin once that environment is gone; the daemon checks.
    func canDelete(_ snapshot: BackupSnapshot) -> Bool {
        if !snapshot.isOrphaned, let origin = environments?.first(where: { $0.name == snapshot.environment }) {
            return origin.role >= .maintain
        }
        return project.access.role == "admin" || isInstanceAdmin
    }

    /// The environments a snapshot may be restored into: maintain on them.
    var restoreTargets: [ProjectEnvironment] {
        (environments ?? []).filter { $0.role >= .maintain && $0.state != "releasing" }
    }
}
