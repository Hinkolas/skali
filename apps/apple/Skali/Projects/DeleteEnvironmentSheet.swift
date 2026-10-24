import SkaliKit
import SwiftUI

/// Deleting the environment on screen: what goes with it, the name typed
/// back, go. One-way; only its snapshots on the backup target remain.
struct DeleteEnvironmentSheet: View {
    let environment: ProjectEnvironment
    let model: ProjectModel

    @Environment(\.dismiss) private var dismiss
    @State private var typed = ""
    @State private var deleting = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    VStack(alignment: .leading, spacing: 8) {
                        Image(systemName: "trash.fill")
                            .font(.largeTitle)
                            .foregroundStyle(.red)
                        Text("Delete \(environment.name)?")
                            .font(.title2.bold())
                        Text("This is one-way. Only the environment's snapshots on the backup target remain, and they can be restored into another environment.")
                            .foregroundStyle(.secondary)
                    }
                }
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets(top: 0, leading: 4, bottom: 4, trailing: 4))

                Section("Deleted With It") {
                    ForEach(stored, id: \.self) { service in
                        StorageServiceRow(service: service)
                    }
                    Label("The namespace and its workloads", systemImage: "square.stack.3d.up")
                    Label("Values, revisions and run history", systemImage: "clock.arrow.circlepath")
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
                    Button(role: .destructive, action: delete) {
                        if deleting {
                            ProgressView().frame(maxWidth: .infinity)
                        } else {
                            Text("Delete \(environment.name)").frame(maxWidth: .infinity)
                        }
                    }
                    .disabled(typed.trimmingCharacters(in: .whitespaces) != environment.name || deleting)
                }
            }
            .navigationTitle("Delete Environment")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(role: .cancel) { dismiss() }
                }
            }
            .alert("Could Not Delete", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
                Button("OK") {}
            } message: {
                Text(error ?? "")
            }
        }
    }

    /// The databases, buckets and volumes whose data goes, largest first.
    private var stored: [ServiceStorage] {
        StorageRollup(storage: model.storage ?? [], environmentID: environment.id).services
            .filter { $0.kind != .temporary }
    }

    private func delete() {
        deleting = true
        Task {
            defer { deleting = false }
            do {
                try await model.deleteEnvironment()
                dismiss()
            } catch is CancellationError {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

/// In place of an environment's pages while it is being deleted: nothing
/// in it is worth acting on anymore, so the page is the delete's progress
/// and the way to another environment.
struct DeletingEnvironmentView: View {
    let environment: ProjectEnvironment
    let model: ProjectModel

    @State private var run: Run?

    var body: some View {
        ContentUnavailableView {
            Label("Deleting \(environment.name)", systemImage: "trash")
        } description: {
            Text("The environment is being deleted with its volumes, databases, buckets, values, revisions and history.")
        } actions: {
            if let run {
                NavigationLink("View Progress", value: ProjectRoute.run(run.id))
                    .buttonStyle(.glassProminent)
            } else {
                ProgressView()
            }
            if let elsewhere {
                Button("Show \(elsewhere.name)") { model.environmentName = elsewhere.name }
            }
        }
        .task(id: environment.id) {
            // The run exists once the decision is taken; give the read a
            // few tries in case the page opened first.
            for _ in 0..<5 where run == nil && !Task.isCancelled {
                run = await model.deletionRun(of: environment)
                if run == nil { try? await Task.sleep(for: .seconds(2)) }
            }
        }
    }

    /// Another environment that is still there.
    private var elsewhere: ProjectEnvironment? {
        model.environments?.first { $0.id != environment.id && !$0.isLocked && $0.state != "releasing" }
    }
}
