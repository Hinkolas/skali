import SkaliKit
import SwiftUI

/// The Settings root: the account, then (for instance admins) the cluster's
/// system areas and its update settings, as the Studio's System hub has them.
struct SettingsView: View {
    @Environment(ClusterSession.self) private var session

    var body: some View {
        List {
            Section {
                NavigationLink {
                    AccountView()
                } label: {
                    AccountRow(user: session.user)
                }
            }
            if session.user.isAdmin {
                systemSection
                UpdateSettingsSection()
            } else {
                Section {
                    LabeledContent("Version", value: session.meta?.version ?? "—")
                        .monospaced()
                }
            }
        }
        .navigationTitle("Settings")
        .task { await session.loadSystem() }
        .refreshable { await session.loadSystem() }
    }

    private var systemSection: some View {
        Section {
            NavigationLink {
                PlaceholderView(title: "Users", systemImage: "person.2")
            } label: {
                Label { Text("Users") } icon: { SettingsIcon(systemImage: "person.2", tint: .serviceApplication) }
            }
            NavigationLink {
                NodesView(nodes: session.system.nodes ?? [])
            } label: {
                LabeledContent {
                    Text(nodesSummary)
                } label: {
                    Label { Text("Nodes") } icon: { SettingsIcon(systemImage: "server.rack", tint: .green) }
                }
            }
            NavigationLink {
                DatabasePoolsView(pools: session.system.pools ?? [])
            } label: {
                LabeledContent {
                    Text(poolsSummary)
                } label: {
                    Label { Text("Databases") } icon: { SettingsIcon(systemImage: "cylinder", tint: .cyan) }
                }
            }
            NavigationLink {
                BackupTargetView(backup: session.system.backup)
            } label: {
                LabeledContent {
                    Text(backupSummary).monospaced()
                } label: {
                    Label { Text("Backup Target") } icon: { SettingsIcon(systemImage: "archivebox", tint: .orange) }
                }
            }
        }
    }

    private var nodesSummary: String {
        guard let nodes = session.system.nodes else { return "" }
        if nodes.isEmpty { return "not observed" }
        let online = nodes.filter(\.ready).count
        return online == nodes.count ? "\(online) online" : "\(online)/\(nodes.count) online"
    }

    private var poolsSummary: String {
        guard let pools = session.system.pools else { return "" }
        switch pools.count {
        case 0: return "no pools"
        case 1: return "1 pool"
        case let count: return "\(count) pools"
        }
    }

    private var backupSummary: String {
        switch session.system.backup {
        case .unknown: ""
        case .unavailable: "unavailable"
        case .none: "not set"
        case .set(let target): target.bucket
        }
    }
}

/// The account card at the top of Settings.
private struct AccountRow: View {
    let user: User

    var body: some View {
        HStack(spacing: 14) {
            Avatar(initials: user.initials, size: 58)
            VStack(alignment: .leading, spacing: 2) {
                Text(user.name.isEmpty ? user.email : user.name)
                    .font(.title3.weight(.semibold))
                Text("Security, sessions & clusters")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 6)
    }
}

/// Software update, auto-install and the release channel. The two settings
/// are sudo-gated on the daemon; the session confirms itself in the browser
/// when the daemon asks for it.
private struct UpdateSettingsSection: View {
    @Environment(ClusterSession.self) private var session
    @State private var saving = false
    @State private var error: String?

    var body: some View {
        Section {
            NavigationLink {
                SoftwareUpdateView()
            } label: {
                LabeledContent {
                    if let updates = session.system.updates, updates.updateAvailable, let latest = updates.latest {
                        Text("\(latest.version) available")
                            .monospaced()
                            .foregroundStyle(.tint)
                    } else {
                        Text(session.system.updates?.installed.version ?? session.meta?.version ?? "")
                            .monospaced()
                    }
                } label: {
                    Label {
                        Text("Software Update")
                    } icon: {
                        SettingsIcon(systemImage: "arrow.triangle.2.circlepath")
                    }
                }
            }
            if let updates = session.system.updates {
                Toggle(isOn: binding(updates, \.autoUpdate)) {
                    Label { Text("Auto-Install Updates") } icon: { SettingsIcon(systemImage: "clock") }
                }
                Picker(selection: binding(updates, \.channel)) {
                    ForEach(ReleaseChannel.allCases) { channel in
                        Text(channel.label).tag(channel)
                    }
                } label: {
                    Label { Text("Release Channel") } icon: { SettingsIcon(systemImage: "arrow.triangle.branch") }
                }
                .pickerStyle(.menu)
            }
        } footer: {
            Text("skalid checks the release feed once a day. Beta includes alpha, beta and release candidates.")
        }
        .disabled(saving)
        .alert("Could Not Save", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
            Button("OK") {}
        } message: {
            Text(error ?? "")
        }
    }

    /// A binding that writes through to the daemon and shows the saved
    /// value; a refused change snaps back.
    private func binding<Value>(
        _ updates: UpdateStatus, _ keyPath: WritableKeyPath<UpdateSettings, Value>
    ) -> Binding<Value> {
        let current = UpdateSettings(channel: updates.channel, autoUpdate: updates.autoUpdate)
        return Binding {
            current[keyPath: keyPath]
        } set: { value in
            var next = current
            next[keyPath: keyPath] = value
            save(next)
        }
    }

    private func save(_ settings: UpdateSettings) {
        saving = true
        Task {
            defer { saving = false }
            do {
                try await session.saveUpdateSettings(settings)
            } catch is CancellationError {
                // The person closed the browser sheet; nothing changed.
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

extension ReleaseChannel {
    var label: String {
        switch self {
        case .stable: "Stable"
        case .beta: "Beta"
        }
    }
}

#Preview {
    PreviewHost { SettingsView() }
}
