import SkaliKit
import SwiftUI

/// One account, as an admin sees it: who it is, what it may do, and the
/// recovery and removal actions. Changes go through Edit, as in Contacts.
struct UserDetailView: View {
    let id: String

    @Environment(ClusterSession.self) private var session
    @Environment(\.dismiss) private var dismiss
    @State private var editing = false
    @State private var resettingPassword = false
    @State private var confirmingDelete = false
    @State private var error: String?

    var body: some View {
        List {
            if let user {
                Section {
                    PersonHeader(user: user, isYou: isYou)
                }
                .listRowBackground(Color.clear)

                Section("Account") {
                    LabeledContent("Role", value: user.isAdmin ? "Instance Admin" : "Member")
                    LabeledContent("Creates Projects", value: user.isAdmin ? "Yes, as an admin" : user.createProjects ? "Yes" : "No")
                    LabeledContent("Two-Factor Authentication", value: user.twoFactorEnabled ? "On" : "Off")
                    LabeledContent("Created", value: user.createdAt, format: .dateTime.day().month().year())
                }

                Section {
                    Button("Reset Password…") { resettingPassword = true }
                } footer: {
                    Text("Sets a new password and signs \(isYou ? "you" : "them") out everywhere. There is no email-based reset.")
                }

                if !isYou {
                    Section {
                        Button("Delete User", role: .destructive) { confirmingDelete = true }
                            .frame(maxWidth: .infinity)
                            .confirmationDialog(
                                "Delete \(displayName(user))?", isPresented: $confirmingDelete, titleVisibility: .visible
                            ) {
                                Button("Delete User", role: .destructive, action: delete)
                            }
                    } footer: {
                        Text("Their account and sessions are removed immediately. Deployed projects are not affected.")
                    }
                }
            }
        }
        .overlay {
            if session.users == nil {
                ProgressView()
            } else if user == nil {
                ContentUnavailableView("User Not Found", systemImage: "person.crop.circle.badge.questionmark")
            }
        }
        .navigationTitle(user.map(displayName) ?? "User")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            if user != nil {
                ToolbarItem(placement: .primaryAction) {
                    Button("Edit") { editing = true }
                }
            }
        }
        .task { if session.users == nil { await session.loadUsers() } }
        .sheet(isPresented: $editing) {
            if let user {
                NavigationStack { EditUserView(user: user, isYou: isYou) }
            }
        }
        .sheet(isPresented: $resettingPassword) {
            if let user {
                NavigationStack { ResetPasswordView(user: user) }
            }
        }
        .alert("Could Not Delete the User", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
            Button("OK") {}
        } message: {
            Text(error ?? "")
        }
    }

    private var user: User? { session.users?.first { $0.id == id } }

    private var isYou: Bool { id == session.user.id }

    private func delete() {
        Task {
            do {
                try await session.deleteUser(id: id)
                dismiss()
            } catch is CancellationError {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

#Preview {
    PreviewHost { UserDetailView(id: DemoCluster.member.id) }
}
