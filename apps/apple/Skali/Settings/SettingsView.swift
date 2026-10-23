import SkaliKit
import SwiftUI

/// The Settings root for instance admins: the account, then the cluster's
/// system areas and its update settings, as the Studio's System hub has
/// them. Members get the account as their tab instead (MainTabView).
struct SettingsView: View {
    @Environment(ClusterSession.self) private var session

    var body: some View {
        List {
            Section {
                NavigationLink(value: SettingsRoute.account) {
                    AccountRow(user: session.user)
                }
            }
            systemSection
            UpdateSettingsSection()
        }
        .navigationTitle("Settings")
        .navigationDestination(for: SettingsRoute.self) { route in
            switch route {
            case .account: AccountView()
            case .users: UsersView()
            case .user(let id): UserDetailView(id: id)
            case .nodes: NodesView()
            case .node(let name): NodeDetailView(name: name)
            case .databases: DatabasePoolsView()
            case .pool(let name): DatabasePoolView(name: name)
            case .backups: BackupsView()
            case .softwareUpdate: SoftwareUpdateView()
            }
        }
        .task { await session.loadSystem() }
        .refreshable { await session.loadSystem() }
    }

    private var systemSection: some View {
        Section {
            NavigationLink(value: SettingsRoute.users) {
                LabeledContent {
                    if let users = session.users { Text("\(users.count) users") }
                } label: {
                    Label { Text("Users") } icon: { SettingsIcon(systemImage: "person.2", tint: .serviceApplication) }
                }
            }
            NavigationLink(value: SettingsRoute.nodes) {
                LabeledContent {
                    Text(nodesSummary)
                } label: {
                    Label { Text("Nodes") } icon: { SettingsIcon(systemImage: "server.rack", tint: .green) }
                }
            }
            NavigationLink(value: SettingsRoute.databases) {
                LabeledContent {
                    Text(poolsSummary)
                } label: {
                    Label { Text("Databases") } icon: { SettingsIcon(systemImage: "cylinder", tint: .cyan) }
                }
            }
            NavigationLink(value: SettingsRoute.backups) {
                LabeledContent {
                    Text(backupSummary).monospaced()
                } label: {
                    Label { Text("Backups") } icon: { SettingsIcon(systemImage: "archivebox", tint: .orange) }
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
            NavigationLink(value: SettingsRoute.softwareUpdate) {
                LabeledContent {
                    updateSummary
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

    /// The row's value: the release, or where an update stands.
    @ViewBuilder private var updateSummary: some View {
        let updates = session.system.updates
        switch updates?.summary.state {
        case .updating:
            Text("Updating · \(updates?.summary.progress.percent ?? 0)%")
                .monospacedDigit()
                .foregroundStyle(.tint)
        case .available:
            Text("\(updates?.summary.targetVersion ?? "") available")
                .monospaced()
                .foregroundStyle(.tint)
        case .failed, .incomplete:
            Text("Needs attention").foregroundStyle(.orange)
        default:
            Text(updates?.installed.version ?? session.meta?.version ?? "")
                .monospaced()
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

/// The screens under Settings, as navigation values so they can be opened
/// from elsewhere (a notification, a deep link) by setting the path.
enum SettingsRoute: Hashable {
    case account, users, nodes, databases, backups, softwareUpdate
    /// One node, by name.
    case node(String)
    /// One database pool, by name.
    case pool(String)
    /// One user, by id.
    case user(String)

    /// Parses `softwareUpdate`, `node:skali-app-04` or `pool:shared`
    /// (debug launch arguments).
    init?(argument: String) {
        if argument.hasPrefix("node:") {
            self = .node(String(argument.dropFirst(5)))
            return
        }
        if argument.hasPrefix("pool:") {
            self = .pool(String(argument.dropFirst(5)))
            return
        }
        if argument.hasPrefix("user:") {
            self = .user(String(argument.dropFirst(5)))
            return
        }
        let simple: [String: SettingsRoute] = [
            "account": .account, "users": .users, "nodes": .nodes, "databases": .databases,
            "backups": .backups, "softwareUpdate": .softwareUpdate,
        ]
        guard let route = simple[argument] else { return nil }
        self = route
    }
}
