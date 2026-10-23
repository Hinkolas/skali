import SkaliKit
import SwiftUI

/// The signed-in account: security, the clusters the app knows, and the
/// account's sessions on the selected cluster. Instance admins reach it
/// from Settings; for members it is the tab itself (`isRoot`).
struct AccountView: View {
    var isRoot = false

    @Environment(AppModel.self) private var model
    @Environment(ClusterSession.self) private var session
    @State private var addingCluster = false
    @State private var confirmingSignOut = false
    @State private var error: String?

    var body: some View {
        List {
            Section {
                AccountHeader(user: session.user)
            }
            .listRowBackground(Color.clear)

            Section {
                NavigationLink {
                    ChangePasswordView()
                } label: {
                    Label { Text("Change Password") } icon: { SettingsIcon(systemImage: "key") }
                }
                NavigationLink {
                    TwoFactorView()
                } label: {
                    LabeledContent {
                        Text(session.user.twoFactorEnabled ? "On" : "Off")
                    } label: {
                        Label { Text("Two-Factor Authentication") } icon: {
                            SettingsIcon(systemImage: "lock.shield")
                        }
                    }
                }
            } footer: {
                Text("Changing your password signs out every other session; only this one survives.")
            }

            clustersSection
            sessionsSection

            Section {
                Button("Sign Out", role: .destructive) { confirmingSignOut = true }
                    .frame(maxWidth: .infinity)
            } footer: {
                Text("Signs this device out of \(session.account.displayName). Other clusters stay signed in.")
            }
        }
        .navigationTitle("Account")
        .navigationBarTitleDisplayMode(isRoot ? .large : .inline)
        .task { await session.loadAccount() }
        .refreshable { await session.loadAccount() }
        .confirmationDialog(
            "Sign out of \(session.account.displayName)?", isPresented: $confirmingSignOut,
            titleVisibility: .visible
        ) {
            Button("Sign Out", role: .destructive) {
                Task { await model.signOut(session.account) }
            }
        }
        .alert("Could Not Sign Out Session", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
            Button("OK") {}
        } message: {
            Text(error ?? "")
        }
        .sheet(isPresented: $addingCluster) {
            NavigationStack {
                SignInView(onDone: { addingCluster = false })
                    .toolbar {
                        ToolbarItem(placement: .cancellationAction) {
                            Button("Cancel", role: .cancel) { addingCluster = false }
                        }
                    }
            }
        }
    }

    private var clustersSection: some View {
        Section {
            ForEach(model.accounts) { account in
                Button {
                    model.select(account.id)
                } label: {
                    ClusterRow(
                        account: account, isSelected: account.id == session.account.id,
                        nodes: account.id == session.account.id ? session.system.nodes : nil)
                }
                .tint(.primary)
            }
            Button {
                addingCluster = true
            } label: {
                Label("Add Cluster", systemImage: "plus")
            }
        } header: {
            Text("Clusters")
        } footer: {
            Text("Tap a cluster to switch. Every tab shows the selected one.")
        }
    }

    private var sessionsSection: some View {
        Section {
            if let sessions = session.sessions {
                ForEach(sessions) { item in
                    NavigationLink {
                        SessionDetailView(item: item)
                    } label: {
                        SessionRow(item: item)
                    }
                    .swipeActions {
                        if !item.isCurrent {
                            Button("Sign Out", role: .destructive) { revoke(item) }
                        }
                    }
                }
            } else if let error = session.sessionsError {
                Text(error).foregroundStyle(.secondary)
            } else {
                ProgressView().frame(maxWidth: .infinity)
            }
        } header: {
            Text("Active Sessions")
        } footer: {
            Text("Swipe a session to sign it out.")
        }
    }

    private func revoke(_ item: Session) {
        Task {
            do {
                try await session.revoke(item)
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

/// Avatar, name, email and the instance role.
private struct AccountHeader: View {
    let user: User

    var body: some View {
        VStack(spacing: 6) {
            Avatar(initials: user.initials, size: 88)
                .padding(.bottom, 6)
            Text(user.name.isEmpty ? user.email : user.name)
                .font(.title.weight(.bold))
                .multilineTextAlignment(.center)
            if !user.name.isEmpty {
                Text(user.email)
                    .foregroundStyle(.secondary)
            }
            Text(user.isAdmin ? "Instance Admin" : "Member")
                .font(.caption.weight(.medium))
                .foregroundStyle(.tint)
                .padding(.horizontal, 10)
                .padding(.vertical, 4)
                .background(Color.accentColor.opacity(0.15), in: .capsule)
                .padding(.top, 4)
        }
        .frame(maxWidth: .infinity)
        .accessibilityElement(children: .combine)
    }
}

/// One cluster the app is signed in to.
private struct ClusterRow: View {
    let account: ClusterAccount
    let isSelected: Bool
    /// The selected cluster's nodes, when the account may see them.
    let nodes: [ClusterNode]?

    @ScaledMetric(relativeTo: .body) private var tile: CGFloat = 34

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: "square.stack.3d.up.fill")
                .font(.system(size: tile * 0.45, weight: .medium))
                .foregroundStyle(.white)
                .frame(width: tile, height: tile)
                .background(isSelected ? Color.accentColor : Color.secondary, in: .rect(cornerRadius: tile * 0.26))
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(account.displayName)
                Text(detail)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer(minLength: 0)
            if isSelected {
                Image(systemName: "checkmark")
                    .font(.body.weight(.semibold))
                    .foregroundStyle(Color.accentColor)
                    .accessibilityLabel("Selected")
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(isSelected ? .isSelected : [])
    }

    private var detail: String {
        var parts = [account.address.host]
        if let nodes, !nodes.isEmpty {
            parts.append("\(nodes.filter(\.ready).count)/\(nodes.count) online")
        } else if !isSelected {
            parts.append(account.user.email)
        }
        return parts.joined(separator: " · ")
    }
}

#Preview("Admin") {
    PreviewHost { AccountView() }
}

#Preview("Member") {
    PreviewHost(demo: DemoCluster(member: true)) { AccountView(isRoot: true) }
}
