import SkaliKit
import SwiftUI
import UIKit

/// A database: how full it is, what it runs on, how to reach it and who
/// does, and how it is backed up.
struct DatabaseServiceView: View {
    let key: String
    let spec: DatabaseSpec
    let model: ProjectModel

    @State private var connection: DatabaseConnection?
    @State private var connectionFailed = false

    var body: some View {
        EnvironmentGate(model: model) { environment in
            List {
                Section {
                    ServiceHeaderView(
                        kind: .database, name: key, state: model.liveState(.database, key),
                        detail: "database · \(spec.engine) \(spec.version)")
                }
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets(top: 0, leading: 4, bottom: 8, trailing: 4))

                Section {
                    let sample = model.storage(of: .database, key).first
                    CapacityCard(
                        title: "Storage", used: sample?.usedBytes, capacity: spec.storageBytes ?? sample?.capacityBytes,
                        tint: .cyan)
                }

                Section("Database") {
                    LabeledContent("Engine") { Text("\(spec.engine) \(spec.version)").monospaced() }
                    LabeledContent("Isolation") { Text(connection?.isolation ?? spec.isolation).monospaced() }
                    LabeledContent("Tier") { Text(connection?.availability ?? spec.availability).monospaced() }
                    // Only while the claim has not settled: the phase names
                    // what it waits for.
                    if let connection, !connection.isProvisioned {
                        LabeledContent("Phase") { Text(connection.phase).monospaced().foregroundStyle(.orange) }
                    }
                }

                Section {
                    ConnectionURLRow(key: key, connection: connection, environment: environment, model: model)
                    ForEach(model.definition?.dependents(of: .database, key) ?? [], id: \.self) { application in
                        NavigationLink(value: ProjectRoute.service(.application, application)) {
                            ConnectedServiceRow(kind: .application, name: application, note: "env injected")
                        }
                    }
                } header: {
                    Text("Connection")
                } footer: {
                    if let connection, let host = connection.host {
                        Text("On the private network at \(host):\(String(connection.port ?? 5432)), database \(connection.database ?? key).")
                    } else if let connection {
                        Text("Not provisioned yet (\(connection.phase)). Connection details appear once the claim settles.")
                    } else if connectionFailed {
                        Text("The connection details could not be read.")
                    }
                }

                Section {
                    ServiceBackupsRow(environment: environment, runs: model.runs)
                }
            }
            .task(id: environment.id) { await loadConnection(environment) }
        }
        .statefulServiceMenu(model, key: key)
    }

    private func loadConnection(_ environment: ProjectEnvironment) async {
        connection = nil
        connectionFailed = false
        do {
            connection = try await model.api.databaseConnection(environmentID: environment.id, key: key)
        } catch is CancellationError {
        } catch {
            connectionFailed = true
        }
    }
}

/// The connection URL, masked until revealed. Revealing reads the
/// credentials once (maintain on the environment, after a fresh sign-in)
/// and keeps them only while the page is open; copying reveals first.
private struct ConnectionURLRow: View {
    let key: String
    let connection: DatabaseConnection?
    let environment: ProjectEnvironment
    let model: ProjectModel

    @State private var credentials: DatabaseCredentials?
    @State private var shown = false
    @State private var working = false
    @State private var copied = false
    @State private var error: String?

    var body: some View {
        HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 4) {
                Text("Connection URL")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                Text(display)
                    .font(.callout.monospaced())
                    .typesetAsCode()
                    .lineLimit(shown ? 5 : 1)
                    .textSelection(.enabled)
            }
            Spacer(minLength: 8)
            if working {
                ProgressView()
            } else {
                Button {
                    if shown { shown = false } else { reveal(thenCopy: false) }
                } label: {
                    Image(systemName: shown ? "eye.slash" : "eye")
                        .frame(width: 36, height: 32)
                }
                .accessibilityLabel(shown ? "Hide Connection URL" : "Reveal Connection URL")
                Button {
                    if let credentials { copy(credentials.url) } else { reveal(thenCopy: true) }
                } label: {
                    Image(systemName: copied ? "checkmark" : "doc.on.doc")
                        .contentTransition(.symbolEffect(.replace))
                        .frame(width: 36, height: 32)
                }
                .accessibilityLabel("Copy Connection URL")
            }
        }
        .buttonStyle(.bordered)
        .buttonBorderShape(.roundedRectangle(radius: 10))
        .tint(.secondary)
        .disabled(!mayReveal)
        .sensoryFeedback(.success, trigger: copied) { _, new in new }
        .task(id: copied) {
            guard copied else { return }
            try? await Task.sleep(for: .seconds(1.5))
            copied = false
        }
        .alert("Could Not Reveal", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
            Button("OK") {}
        } message: {
            Text(error ?? "")
        }
        .onChange(of: environment.id) {
            credentials = nil
            shown = false
        }
    }

    /// Maintain on the environment reveals; nothing to reveal before the
    /// claim is provisioned.
    private var mayReveal: Bool {
        environment.role >= .maintain && connection?.isProvisioned == true
    }

    private var display: String {
        if shown, let credentials { return credentials.url.wrappingAtSeams }
        return "\(connection?.engine ?? "postgres")://••••••••••••"
    }

    private func reveal(thenCopy: Bool) {
        working = true
        Task {
            defer { working = false }
            do {
                let revealed = try await model.revealDatabaseCredentials(key)
                credentials = revealed
                if thenCopy { copy(revealed.url) } else { shown = true }
            } catch is CancellationError {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }

    private func copy(_ url: String) {
        UIPasteboard.general.setItems([[UIPasteboard.typeAutomatic: url]], options: [
            .expirationDate: Date.now.addingTimeInterval(120),
        ])
        copied = true
    }
}
