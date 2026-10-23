import SkaliKit
import SwiftUI

/// Every account on the cluster: admins, then members, searchable by name
/// or email, with a new user one tap away.
struct UsersView: View {
    @Environment(ClusterSession.self) private var session
    @State private var query = ""
    @State private var addingUser = false
    @State private var deleting: User?
    @State private var error: String?

    var body: some View {
        let users = filtered
        List {
            section("Admins", users.filter(\.isAdmin))
            section("Members", users.filter { !$0.isAdmin })
        }
        .overlay {
            if session.users == nil {
                ProgressView()
            } else if users.isEmpty && !query.isEmpty {
                ContentUnavailableView.search(text: query)
            }
        }
        .navigationTitle("Users")
        .largeSubtitle(subtitle)
        .searchable(text: $query, prompt: "Name or email")
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button("Add User", systemImage: "plus") { addingUser = true }
            }
        }
        .task { await session.loadUsers() }
        .refreshable { await session.loadUsers() }
        .sheet(isPresented: $addingUser) {
            NavigationStack { NewUserView() }
        }
        // An alert, not a dialog: a swipe action's dialog would point at
        // the list rather than the row, and the closing swipe dismisses it.
        .alert(
            "Delete \(deleting.map(displayName) ?? "User")?",
            isPresented: Binding { deleting != nil } set: { if !$0 { deleting = nil } },
            presenting: deleting
        ) { user in
            Button("Delete", role: .destructive) { delete(user) }
            Button("Cancel", role: .cancel) {}
        } message: { _ in
            Text("Their account and sessions are removed immediately. Deployed projects are not affected.")
        }
        .alert("Could Not Delete the User", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
            Button("OK") {}
        } message: {
            Text(error ?? "")
        }
    }

    @ViewBuilder private func section(_ title: String, _ users: [User]) -> some View {
        if !users.isEmpty {
            Section("\(title) · \(users.count)") {
                ForEach(users) { user in
                    NavigationLink(value: SettingsRoute.user(user.id)) {
                        UserRow(user: user, isYou: user.id == session.user.id)
                    }
                    .swipeActions {
                        if user.id != session.user.id {
                            Button("Delete", systemImage: "trash", role: .destructive) { deleting = user }
                        }
                    }

                }
            }
        }
    }

    /// By name (or email when there is none), filtered by the search.
    private var filtered: [User] {
        let users = (session.users ?? []).sorted {
            displayName($0).localizedStandardCompare(displayName($1)) == .orderedAscending
        }
        guard !query.isEmpty else { return users }
        return users.filter { $0.name.localizedStandardContains(query) || $0.email.localizedStandardContains(query) }
    }

    /// "7 users · 2 admins".
    private var subtitle: String {
        guard let users = session.users else { return "" }
        let admins = users.count(where: \.isAdmin)
        return "\(users.count == 1 ? "1 user" : "\(users.count) users") · \(admins == 1 ? "1 admin" : "\(admins) admins")"
    }

    private func delete(_ user: User) {
        Task {
            do {
                try await session.deleteUser(id: user.id)
            } catch is CancellationError {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

func displayName(_ user: User) -> String {
    user.name.isEmpty ? user.email : user.name
}

/// One account: avatar, name and email, and what sets it apart.
private struct UserRow: View {
    let user: User
    let isYou: Bool

    @ScaledMetric(relativeTo: .headline) private var avatar: CGFloat = 40

    var body: some View {
        HStack(spacing: 12) {
            Avatar(initials: user.initials, size: avatar)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(displayName(user))
                        .font(.headline)
                        .lineLimit(1)
                    if isYou { RoleChip(text: "You", tint: .secondary).font(.caption2) }
                }
                Text(user.name.isEmpty ? "No name" : user.email)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer(minLength: 8)
            HStack(spacing: 8) {
                if !user.isAdmin && user.createProjects {
                    Image(systemName: "folder.badge.plus")
                        .foregroundStyle(.secondary)
                        .accessibilityLabel("Creates projects")
                }
                if user.twoFactorEnabled {
                    Image(systemName: "checkmark.shield.fill")
                        .foregroundStyle(.green)
                        .accessibilityLabel("Two-factor authentication on")
                }
            }
            .font(.subheadline)
        }
        .padding(.vertical, 2)
        .accessibilityElement(children: .combine)
    }
}

#Preview {
    PreviewHost { UsersView() }
}
