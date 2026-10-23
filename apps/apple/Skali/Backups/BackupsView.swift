import SkaliKit
import SwiftUI

/// Backups: what the backup target holds and how backups have been
/// running, or, without a target, the way to configure one.
struct BackupsView: View {
    @Environment(ClusterSession.self) private var session
    @State private var editing: BackupTarget?
    @State private var configuring = false

    var body: some View {
        content
            .navigationTitle("Backups")
            .navigationBarTitleDisplayMode(.inline)
            .task { await session.loadBackups() }
            .refreshable { await session.loadBackups() }
            .sheet(isPresented: $configuring) {
                NavigationStack { BackupTargetForm(target: nil) }
            }
            .sheet(item: $editing) { target in
                NavigationStack { BackupTargetForm(target: target) }
            }
    }

    @ViewBuilder private var content: some View {
        switch session.backups {
        case .unknown:
            ProgressView()
        case .failed(let message):
            ContentUnavailableView {
                Label("Backups Unavailable", systemImage: "exclamationmark.triangle")
            } description: {
                Text(message)
            } actions: {
                Button("Try Again") { Task { await session.loadBackups() } }
            }
        case .notConfigured:
            ContentUnavailableView {
                Label("No Backup Target", systemImage: "archivebox")
            } description: {
                Text("Backups are copied to an S3 bucket you own, off the cluster. Configure one to back up environments and restore them.")
            } actions: {
                Button("Configure Backup Target") { configuring = true }
                    .buttonStyle(.glassProminent)
                    .controlSize(.large)
            }
        case .summary(let summary):
            BackupOverview(summary: summary, edit: { editing = summary.target })
        case .targetOnly(let target):
            List {
                Section {
                    BackupHeader(target: target, reachable: nil)
                }
                .listRowBackground(Color.clear)
                BackupTargetSection(target: target, edit: { editing = target })
            }
        }
    }
}

/// The bucket with its state.
private struct BackupHeader: View {
    let target: BackupTarget
    /// Nil when the daemon offers no summary to tell.
    let reachable: Bool?

    var body: some View {
        ResourceHeader(
            systemImage: "archivebox", tint: reachable == false ? .red : .orange, name: target.bucket,
            status: StatusCapsule(
                text: reachable == nil ? "Configured" : reachable == true ? "Connected" : "Unreachable",
                tint: reachable == nil ? .secondary : reachable == true ? .green : .red),
            detail: target.endpointHost)
    }
}

/// A configured target's overview.
private struct BackupOverview: View {
    let summary: BackupSummary
    let edit: () -> Void

    var body: some View {
        List {
            Section {
                BackupHeader(target: summary.target, reachable: summary.reachable)
            }
            .listRowBackground(Color.clear)

            if !summary.reachable {
                Section {
                    Label {
                        VStack(alignment: .leading, spacing: 4) {
                            Text("The Bucket Does Not Answer").font(.headline)
                            if let reason = summary.unreachableReason {
                                Text(reason).font(.caption.monospaced()).foregroundStyle(.secondary)
                            }
                            Text("Backups and restores fail until it does. Check the endpoint, the bucket and the credentials.")
                                .font(.subheadline).foregroundStyle(.secondary)
                        }
                    } icon: {
                        Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.red)
                    }
                }
            }

            Section {
                tiles
            }
            .listRowBackground(Color.clear)
            .listRowInsets(EdgeInsets())

            activitySection
            if let snapshots = summary.snapshots {
                projectsSection(snapshots)
                snapshotsSection(snapshots)
            }
            BackupTargetSection(target: summary.target, edit: edit)
        }
    }

    // MARK: Headlines

    private var tiles: some View {
        let snapshots = summary.snapshots
        return UsageTileRow {
            UsageTile(
                title: "Stored", value: snapshots.map { formatBytes($0.bytes) } ?? "—", fraction: nil,
                color: .usageDisk, caption: "in snapshots", showsBar: false)
            UsageTile(
                title: "Snapshots", value: snapshots.map { "\($0.count)" } ?? "—", fraction: nil,
                color: .usageDisk,
                caption: snapshots.map { "\($0.projects.count) \($0.projects.count == 1 ? "project" : "projects")" } ?? "unknown",
                showsBar: false)
            UsageTile(
                title: "Last Backup",
                value: summary.runs.lastSucceeded.map { "\(compactAge(since: $0.finishedAt)) ago" } ?? "never",
                fraction: nil, color: .usageDisk,
                caption: summary.runs.lastSucceeded?.project ?? "no backup yet", showsBar: false)
        }
    }

    // MARK: Activity

    private var activitySection: some View {
        let runs = summary.runs
        return Section {
            LabeledContent("Succeeded") {
                Text("\(runs.succeeded)").monospacedDigit()
            }
            LabeledContent("Failed") {
                Text("\(runs.failed)").monospacedDigit()
                    .foregroundStyle(runs.failed > 0 ? Color.red : .secondary)
            }
            if runs.backingUp + runs.restoring > 0 {
                LabeledContent("Running Now") {
                    HStack(spacing: 6) {
                        ProgressView().controlSize(.small)
                        Text(runningText(runs))
                    }
                }
            }
            if let failure = runs.lastFailed, summary.failingNow || failure.finishedAt > runs.since {
                VStack(alignment: .leading, spacing: 4) {
                    HStack {
                        Text("Last Failure")
                        Spacer()
                        Text(failure.finishedAt, format: .relative(presentation: .named))
                            .foregroundStyle(.secondary)
                    }
                    Text("\(failure.project) · \(failure.environment)")
                        .font(.subheadline.monospaced())
                        .foregroundStyle(.secondary)
                    if let error = failure.error {
                        Text(error)
                            .font(.caption.monospaced())
                            .foregroundStyle(.red)
                            .textSelection(.enabled)
                    }
                }
                .padding(.vertical, 2)
            }
            LabeledContent("Automatic Backups") {
                Text("\(summary.schedules.scheduled) of \(summary.schedules.environments) environments")
            }
        } header: {
            Text("Last 7 Days")
        } footer: {
            Text("Automatic backups are set per environment in the Studio.")
        }
    }

    private func runningText(_ runs: BackupSummary.Runs) -> String {
        var parts: [String] = []
        if runs.backingUp > 0 { parts.append(runs.backingUp == 1 ? "1 backup" : "\(runs.backingUp) backups") }
        if runs.restoring > 0 { parts.append(runs.restoring == 1 ? "1 restore" : "\(runs.restoring) restores") }
        return parts.joined(separator: ", ")
    }

    // MARK: Contents

    private func projectsSection(_ snapshots: BackupSummary.Snapshots) -> some View {
        Section {
            if snapshots.projects.isEmpty {
                Text("No snapshots yet.").foregroundStyle(.secondary)
            }
            ForEach(snapshots.projects) { project in
                ProjectStorageRow(project: project, share: snapshots.bytes > 0 ? Double(project.bytes) / Double(snapshots.bytes) : 0)
            }
        } header: {
            Text("By Project")
        }
    }

    private func snapshotsSection(_ snapshots: BackupSummary.Snapshots) -> some View {
        Section {
            LabeledContent("Scheduled", value: "\(snapshots.scheduled)")
            LabeledContent("Manual", value: "\(snapshots.manual)")
            if snapshots.orphaned > 0 {
                LabeledContent("From Deleted Environments", value: "\(snapshots.orphaned)")
            }
            if let oldest = snapshots.oldestAt {
                LabeledContent("Oldest", value: oldest, format: .dateTime.day().month().year())
            }
        } header: {
            Text("Snapshots")
        } footer: {
            Text("Counted from the snapshots' manifests at \(summary.checkedAt.formatted(date: .omitted, time: .shortened)). Scheduled snapshots expire under their environment's retention; manual ones stay until deleted.")
        }
    }
}

/// One project's share of what the target stores.
private struct ProjectStorageRow: View {
    let project: BackupSummary.Snapshots.Project
    let share: Double

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline) {
                Text(project.project)
                    .font(.body.monospaced())
                    .lineLimit(1)
                    .truncationMode(.middle)
                Spacer(minLength: 8)
                Text(formatBytes(project.bytes))
                    .font(.subheadline.monospacedDigit())
            }
            UsageBar(fraction: share, color: .usageDisk)
            Text("\(project.count == 1 ? "1 snapshot" : "\(project.count) snapshots") · newest \(project.newestAt.formatted(.relative(presentation: .named)))")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .padding(.vertical, 2)
        .accessibilityElement(children: .combine)
    }
}

/// Where the target is, and changing or removing it.
private struct BackupTargetSection: View {
    let target: BackupTarget
    let edit: () -> Void

    @Environment(ClusterSession.self) private var session
    @State private var confirmingRemove = false
    @State private var error: String?

    var body: some View {
        Section {
            LabeledContent("Endpoint") {
                Text(target.endpoint).lineLimit(1).truncationMode(.middle).textSelection(.enabled)
            }
            if !target.region.isEmpty {
                LabeledContent("Region") { Text(target.region).monospaced() }
            }
            LabeledContent("Bucket") { Text(target.bucket).monospaced() }
            LabeledContent("Prefix") { Text(target.prefix.isEmpty ? "—" : target.prefix).monospaced() }
            LabeledContent("Access Key ID") {
                Text(target.accessKeyId).monospaced().lineLimit(1).truncationMode(.middle)
            }
            if let updated = target.updatedAt {
                LabeledContent("Last Changed", value: updated, format: .dateTime.day().month().year())
            }
            Button("Edit Target…", action: edit)
        } header: {
            Text("Target")
        } footer: {
            Text("The secret access key is stored encrypted and never shown again.")
        }

        Section {
            Button("Remove Target", role: .destructive) { confirmingRemove = true }
                .frame(maxWidth: .infinity)
                .confirmationDialog("Remove the backup target?", isPresented: $confirmingRemove, titleVisibility: .visible) {
                    Button("Remove Target", role: .destructive, action: remove)
                } message: {
                    Text("Snapshots already in the bucket stay there. Configuring the same location again makes them listable and restorable once more.")
                }
        } footer: {
            Text("Backups and restores stop until a target is configured again.")
        }
        .alert("Could Not Remove the Target", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
            Button("OK") {}
        } message: {
            Text(error ?? "")
        }
    }

    private func remove() {
        Task {
            do {
                try await session.removeBackupTarget()
            } catch is CancellationError {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

extension BackupTarget: @retroactive Identifiable {
    public var id: String { name }
}

#Preview("Configured") {
    PreviewHost { BackupsView() }
}

#Preview("Not configured") {
    PreviewHost(demo: DemoCluster(backups: .none)) { BackupsView() }
}
