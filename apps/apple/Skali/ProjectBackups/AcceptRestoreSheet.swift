import SkaliKit
import SwiftUI

/// The hold an incomplete restore keeps on the environment, on its
/// overview: while it runs, that it does; once it failed or was
/// cancelled, both ways forward. Restoring again is the way back to
/// complete data; accepting takes the data as it is.
struct RestoreHoldSection: View {
    let restore: IncompleteRestore
    let environment: ProjectEnvironment
    let onAccept: () -> Void

    var body: some View {
        Section {
            VStack(alignment: .leading, spacing: 6) {
                Label(title, systemImage: "exclamationmark.triangle.fill")
                    .font(.headline)
                    .foregroundStyle(restore.isRunning ? .orange : .red)
                Text(message)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                if let error = restore.error, !restore.isRunning {
                    Text(error)
                        .font(.caption.monospaced())
                        .typesetAsCode()
                        .foregroundStyle(.secondary)
                }
            }
            .padding(.vertical, 4)

            if let runID = restore.runId {
                NavigationLink(value: ProjectRoute.run(runID)) {
                    Label("View Run", systemImage: "list.bullet.rectangle.portrait")
                }
            }
            if !restore.isRunning {
                NavigationLink(value: ProjectRoute.backups) {
                    Label("Restore Again", systemImage: "arrow.counterclockwise")
                }
                Button(role: .destructive, action: onAccept) {
                    Label("Accept Current Data…", systemImage: "checkmark.shield")
                        .foregroundStyle(.red)
                }
                .disabled(environment.role < .maintain)
            }
        } footer: {
            if !restore.isRunning && environment.role < .maintain {
                Text("Accepting the data needs maintain access to \(environment.name).")
            }
        }
    }

    private var title: String {
        if restore.isRunning { return "Restore Running" }
        return restore.status == "cancelled" ? "Restore Cancelled" : "Restore Failed"
    }

    private var message: String {
        let snapshot = restore.snapshotId.prefix(8)
        if restore.isRunning {
            return "Snapshot \(snapshot) is replacing \(environment.name)'s data. The environment is down meanwhile, and deploys wait until the restore completes."
        }
        let when = compactAge(since: restore.finishedAt ?? restore.startedAt)
        let partial = restore.restoredComponents.isEmpty
            ? "Its data may be partial"
            : "\(restore.restoredComponents.map(\.label).formatted(.list(type: .and))) may hold partial data"
        return "The restore of snapshot \(snapshot) \(restore.outcome) \(when) ago. \(partial), so \(environment.name) stays down and refuses deploys until a restore completes or the data is accepted as it is."
    }
}

/// Accepting the data an incomplete restore left: what may be partial,
/// what accepting does, the name typed back, go. Deploys are allowed
/// again; the environment stays down until the next one.
struct AcceptRestoreSheet: View {
    let restore: IncompleteRestore
    let environment: ProjectEnvironment
    let model: ProjectModel

    @Environment(\.dismiss) private var dismiss
    @State private var typed = ""
    @State private var accepting = false
    @State private var error: String?

    @ScaledMetric(relativeTo: .body) private var tile: CGFloat = 32

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    VStack(alignment: .leading, spacing: 8) {
                        Image(systemName: "exclamationmark.triangle.fill")
                            .font(.largeTitle)
                            .foregroundStyle(.red)
                        Text("Accept the Data in \(environment.name)?")
                            .font(.title2.bold())
                        Text("The restore of snapshot \(restore.snapshotId.prefix(8)) started \(restore.startedAt.formatted(date: .abbreviated, time: .shortened)) and \(restore.outcome).")
                            .foregroundStyle(.secondary)
                    }
                }
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets(top: 0, leading: 4, bottom: 4, trailing: 4))

                let components = restore.restoredComponents
                if !components.isEmpty {
                    Section {
                        ForEach(components, id: \.self) { component in
                            HStack(spacing: 12) {
                                ResourceTile(systemImage: component.volume == nil ? component.kind.systemImage : "externaldrive",
                                             tint: component.kind.tint, size: tile)
                                Text(component.label).monospaced()
                                Spacer(minLength: 8)
                                Text(component.volume == nil ? component.kind.rawValue : "volume")
                                    .font(.caption.monospaced())
                                    .foregroundStyle(.secondary)
                            }
                        }
                    } header: {
                        Text("May Hold Partial Data")
                    } footer: {
                        Text("What the restore never reached still holds its data from before. Restoring again is the way back to complete data.")
                    }
                }

                Section {
                    Label {
                        Text("Accepting allows deploys again, but \(environment.name) stays down until the next deployment brings it back on this data: a promotion from another environment, or `skali deploy`.")
                    } icon: {
                        Image(systemName: "arrow.uturn.forward")
                            .foregroundStyle(.secondary)
                    }
                    .font(.subheadline)
                }

                Section {
                    TextField(environment.name, text: $typed)
                        .monospaced()
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                } header: {
                    Text("Type \(environment.name) to confirm")
                }

                Section {
                    Button(role: .destructive, action: accept) {
                        if accepting {
                            ProgressView().frame(maxWidth: .infinity)
                        } else {
                            Text("Accept Current Data").frame(maxWidth: .infinity)
                        }
                    }
                    .disabled(typed.trimmingCharacters(in: .whitespaces) != environment.name || accepting)
                }
            }
            .navigationTitle("Accept Restore")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(role: .cancel) { dismiss() }
                }
            }
            .alert("Could Not Accept", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
                Button("OK") {}
            } message: {
                Text(error ?? "")
            }
        }
    }

    private func accept() {
        accepting = true
        Task {
            defer { accepting = false }
            do {
                try await model.acceptRestore(restore)
                dismiss()
            } catch is CancellationError {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}
