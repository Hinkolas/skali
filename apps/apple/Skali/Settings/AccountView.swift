import SkaliKit
import SwiftUI

/// The signed-in account, the clusters the app knows, and signing out.
struct AccountView: View {
    @Environment(AppModel.self) private var model
    @Environment(ClusterSession.self) private var session
    @State private var addingCluster = false
    @State private var confirmingSignOut = false

    var body: some View {
        List {
            Section {
                VStack(spacing: 8) {
                    Avatar(initials: session.user.initials, size: 84)
                    Text(session.user.name).font(.title2.weight(.semibold))
                    Text(session.user.email).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity)
                .listRowBackground(Color.clear)
            }
            Section("Security") {
                LabeledContent("Role", value: session.user.isAdmin ? "Instance Admin" : "Member")
                LabeledContent("Two-Factor Authentication", value: session.user.twoFactorEnabled ? "On" : "Off")
            }
            Section("Clusters") {
                ForEach(model.accounts) { account in
                    Button {
                        model.select(account.id)
                    } label: {
                        LabeledContent {
                            if account.id == session.account.id {
                                Image(systemName: "checkmark").foregroundStyle(.tint)
                            }
                        } label: {
                            Text(account.displayName)
                            Text("\(account.user.email) · \(account.address.host)")
                        }
                    }
                    .tint(.primary)
                }
                Button("Add Cluster…", systemImage: "plus") { addingCluster = true }
            }
            Section {
                Button("Sign Out of \(session.account.displayName)", role: .destructive) {
                    confirmingSignOut = true
                }
                .confirmationDialog("Sign out of \(session.account.displayName)?", isPresented: $confirmingSignOut) {
                    Button("Sign Out", role: .destructive) {
                        Task { await model.signOut(session.account) }
                    }
                }
            }
        }
        .navigationTitle("Account")
        .navigationBarTitleDisplayMode(.inline)
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
}
