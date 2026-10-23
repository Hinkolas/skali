import SkaliKit
import SwiftUI

/// Changes the account password. The daemon asks for a recent sign-in
/// first (the browser sheet confirms it) and signs every other session out.
struct ChangePasswordView: View {
    @Environment(ClusterSession.self) private var session
    @Environment(\.dismiss) private var dismiss
    @State private var current = ""
    @State private var new = ""
    @State private var confirmation = ""
    @State private var saving = false
    @State private var error: String?
    @FocusState private var focus: Field?

    private enum Field { case current, new, confirmation }

    /// The daemon's minimum (internal/auth, ErrWeakPassword).
    private let minimumLength = 8

    var body: some View {
        Form {
            Section {
                SecureField("Current Password", text: $current)
                    .textContentType(.password)
                    .focused($focus, equals: .current)
                    .submitLabel(.next)
                    .onSubmit { focus = .new }
            }
            Section {
                SecureField("New Password", text: $new)
                    .textContentType(.newPassword)
                    .focused($focus, equals: .new)
                    .submitLabel(.next)
                    .onSubmit { focus = .confirmation }
                SecureField("Confirm New Password", text: $confirmation)
                    .textContentType(.newPassword)
                    .focused($focus, equals: .confirmation)
                    .submitLabel(.done)
                    .onSubmit(save)
            } footer: {
                if let error {
                    Text(error).foregroundStyle(.red)
                } else if !confirmation.isEmpty && confirmation != new {
                    Text("The passwords do not match.").foregroundStyle(.red)
                } else {
                    Text("At least \(minimumLength) characters. Every other session of this account is signed out.")
                }
            }
        }
        .navigationTitle("Change Password")
        .navigationBarTitleDisplayMode(.inline)
        .disabled(saving)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                if saving {
                    ProgressView()
                } else {
                    Button("Save", action: save).disabled(!isValid)
                }
            }
        }
        .onAppear { focus = .current }
    }

    private var isValid: Bool {
        !current.isEmpty && new.count >= minimumLength && new == confirmation
    }

    private func save() {
        guard isValid, !saving else { return }
        saving = true
        error = nil
        Task {
            defer { saving = false }
            do {
                try await session.changePassword(current: current, new: new)
                dismiss()
            } catch is CancellationError {
                // The browser sheet was closed; nothing changed.
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}
