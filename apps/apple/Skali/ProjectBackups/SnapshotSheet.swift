import SkaliKit
import SwiftUI
import UIKit

/// One snapshot: where and when it was taken, what it holds, and restoring
/// or deleting it. A restore, once started, shows its run here.
struct SnapshotSheet: View {
    let snapshot: BackupSnapshot
    let model: ProjectModel

    private enum Route: Hashable {
        case restore
        case run(String)
    }

    @Environment(\.dismiss) private var dismiss
    @State private var path: [Route] = []
    @State private var confirmingDelete = false
    @State private var deleting = false
    @State private var error: String?

    var body: some View {
        NavigationStack(path: $path) {
            List {
                Section {
                    VStack(alignment: .leading, spacing: 6) {
                        Text(snapshot.createdAt, format: .dateTime.month(.wide).day().year().hour().minute())
                            .font(.title2.bold())
                        Text("\(snapshot.isScheduled ? "scheduled" : "manual") · \(snapshot.environment)\(snapshot.isOrphaned ? " (deleted)" : "")")
                            .font(.footnote.monospaced())
                            .foregroundStyle(.secondary)
                    }
                }
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets(top: 0, leading: 4, bottom: 4, trailing: 4))

                Section {
                    LabeledContent("Environment") { Text(snapshot.environment).monospaced() }
                    LabeledContent("Taken", value: snapshot.createdAt, format: .relative(presentation: .named))
                    LabeledContent("Kept") {
                        Text(snapshot.isScheduled ? "Expires under the retention" : "Until deleted")
                    }
                    LabeledContent("Contents", value: snapshot.contents)
                    LabeledContent("Size", value: formatBytes(snapshot.bytes))
                    LabeledContent("Revision") { Text(snapshot.shortRevision).monospaced() }
                    LabeledContent("Snapshot ID") {
                        Text(snapshot.id.prefix(8)).monospaced()
                    }
                    .contextMenu {
                        Button("Copy Snapshot ID", systemImage: "doc.on.doc") { UIPasteboard.general.string = snapshot.id }
                    }
                } footer: {
                    if snapshot.isOrphaned {
                        Text("The environment it was taken from no longer exists. Its data can still be restored into another environment.")
                    }
                }

                Section {
                    Button("Restore…", systemImage: "arrow.counterclockwise") { path.append(.restore) }
                        .disabled(model.restoreTargets.isEmpty)
                    Button("Delete Snapshot", systemImage: "trash", role: .destructive) { confirmingDelete = true }
                        .disabled(!model.canDelete(snapshot) || deleting)
                        .confirmationDialog(
                            "Delete this snapshot?", isPresented: $confirmingDelete, titleVisibility: .visible
                        ) {
                            Button("Delete Snapshot", role: .destructive) { delete() }
                        } message: {
                            Text("Its data is removed from the backup target and cannot be restored afterwards.")
                        }
                } footer: {
                    if model.restoreTargets.isEmpty {
                        Text("Restoring needs maintain access to an environment of the project.")
                    } else if !model.canDelete(snapshot) {
                        Text(snapshot.isOrphaned
                             ? "Deleting it needs admin access to the project."
                             : "Deleting it needs maintain access to \(snapshot.environment).")
                    }
                }
            }
            .navigationTitle("Snapshot")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button(role: .close) { dismiss() }
                }
            }
            .navigationDestination(for: Route.self) { route in
                switch route {
                case .restore:
                    RestoreView(snapshot: snapshot, model: model) { runID in path = [.run(runID)] }
                case .run(let id):
                    RunDetailView(runID: id, model: model)
                }
            }
            .alert("Could Not Delete", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
                Button("OK") {}
            } message: {
                Text(error ?? "")
            }
        }
    }

    private func delete() {
        deleting = true
        Task {
            defer { deleting = false }
            do {
                try await model.deleteSnapshot(snapshot)
                dismiss()
            } catch is CancellationError {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

/// Restoring a snapshot: pick the environment it goes into (its origin
/// when that may be written), read what happens, type the name, go.
private struct RestoreView: View {
    let snapshot: BackupSnapshot
    let model: ProjectModel
    let onStarted: (String) -> Void

    @State private var targetID: String?
    @State private var typed = ""
    @State private var restoring = false
    @State private var error: String?

    var body: some View {
        Form {
            Section {
                ForEach(model.environments ?? []) { environment in
                    let reason = refusal(environment)
                    Button {
                        targetID = environment.id
                        typed = ""
                    } label: {
                        HStack {
                            VStack(alignment: .leading, spacing: 2) {
                                HStack(spacing: 6) {
                                    Text(environment.name).monospaced()
                                    if environment.name == snapshot.environment && !snapshot.isOrphaned {
                                        TagChip(text: "origin")
                                    }
                                }
                                if let reason {
                                    Text(reason)
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                }
                            }
                            Spacer()
                            if targetID == environment.id {
                                Image(systemName: "checkmark")
                                    .foregroundStyle(.tint)
                                    .fontWeight(.semibold)
                            }
                        }
                    }
                    .tint(.primary)
                    .disabled(reason != nil)
                    .accessibilityAddTraits(targetID == environment.id ? .isSelected : [])
                }
            } header: {
                Text("Restore Into")
            }

            if let target {
                Section {
                    Label {
                        Text("Restoring stops \(target.name), replaces its databases, buckets and volumes with the snapshot's data, then starts the current revision again. If it fails after the stop, \(target.name) stays down and refuses deploys until a restore completes or its data is accepted.")
                    } icon: {
                        Image(systemName: "exclamationmark.triangle.fill")
                            .foregroundStyle(.red)
                    }
                    .font(.subheadline)
                    if target.name != snapshot.environment || snapshot.isOrphaned {
                        Label("This snapshot was taken from \(snapshot.environment), not \(target.name).",
                              systemImage: "arrow.left.arrow.right")
                            .font(.subheadline)
                            .foregroundStyle(.orange)
                    }
                }

                Section {
                    TextField(target.name, text: $typed)
                        .monospaced()
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                } header: {
                    Text("Type \(target.name) to confirm")
                }

                Section {
                    Button(role: .destructive) {
                        restore(into: target)
                    } label: {
                        if restoring {
                            ProgressView().frame(maxWidth: .infinity)
                        } else {
                            Text("Restore into \(target.name)").frame(maxWidth: .infinity)
                        }
                    }
                    .disabled(typed.trimmingCharacters(in: .whitespaces) != target.name || restoring)
                }
            }
        }
        .navigationTitle("Restore Snapshot")
        .navigationBarTitleDisplayMode(.inline)
        .onAppear {
            guard targetID == nil else { return }
            // The origin, when it still exists and may be written.
            if !snapshot.isOrphaned,
               let origin = model.environments?.first(where: { $0.name == snapshot.environment }),
               refusal(origin) == nil {
                targetID = origin.id
            }
        }
        .alert("Could Not Restore", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
            Button("OK") {}
        } message: {
            Text(error ?? "")
        }
    }

    private var target: ProjectEnvironment? {
        model.environments?.first { $0.id == targetID }
    }

    private func refusal(_ environment: ProjectEnvironment) -> String? {
        if environment.isLocked { return "locked for you" }
        if environment.state == "releasing" { return "being deleted" }
        if environment.role < .maintain { return "maintain access required" }
        return nil
    }

    private func restore(into target: ProjectEnvironment) {
        restoring = true
        Task {
            defer { restoring = false }
            do {
                onStarted(try await model.restore(snapshot, into: target))
            } catch is CancellationError {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}
