import SkaliKit
import SwiftUI

/// Provisions an account. The server sends no invitation, so the form
/// ends on the sign-in details to hand over.
struct NewUserView: View {
    @Environment(ClusterSession.self) private var session
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var email = ""
    @State private var isAdmin = false
    @State private var createProjects = false
    @State private var password = PasswordGenerator.generate()
    @State private var saving = false
    @State private var error: String?
    @State private var created: User?

    var body: some View {
        if let created {
            SignInDetailsView(user: created, password: password, action: .created) { dismiss() }
        } else {
            form
        }
    }

    private var form: some View {
        Form {
            Section {
                TextField("Name", text: $name)
                    .textContentType(.name)
                TextField("Email", text: $email)
                    .keyboardType(.emailAddress)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
            }
            Section {
                Picker("Role", selection: $isAdmin) {
                    Text("Member").tag(false)
                    Text("Instance Admin").tag(true)
                }
                if !isAdmin {
                    Toggle("Can Create Projects", isOn: $createProjects)
                }
            } footer: {
                Text(isAdmin
                     ? "Admins manage users, nodes and every project."
                     : "Members see the projects they are granted, and become admin of the projects they create.")
            }
            Section {
                PasswordField(password: $password)
            } header: {
                Text("Initial Password")
            } footer: {
                if let error {
                    Text(error).foregroundStyle(.red)
                } else {
                    Text("At least 8 characters. You hand it over next; they can change it under Account.")
                }
            }
        }
        .navigationTitle("New User")
        .navigationBarTitleDisplayMode(.inline)
        .disabled(saving)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Cancel", role: .cancel) { dismiss() }
            }
            ToolbarItem(placement: .confirmationAction) {
                if saving {
                    ProgressView()
                } else {
                    Button("Add", action: save).disabled(!isValid)
                }
            }
        }
    }

    private var isValid: Bool {
        email.contains("@") && password.count >= 8
    }

    private func save() {
        saving = true
        error = nil
        Task {
            defer { saving = false }
            do {
                created = try await session.createUser(NewUser(
                    email: email.trimmingCharacters(in: .whitespaces), name: name.trimmingCharacters(in: .whitespaces),
                    password: password, role: isAdmin ? "admin" : "member", createProjects: !isAdmin && createProjects))
            } catch is CancellationError {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

/// Changes a name, a role or the right to create projects. Your own role
/// is locked; another admin has to change it.
struct EditUserView: View {
    let user: User
    let isYou: Bool

    @Environment(ClusterSession.self) private var session
    @Environment(\.dismiss) private var dismiss
    @State private var name: String
    @State private var isAdmin: Bool
    @State private var createProjects: Bool
    @State private var saving = false
    @State private var error: String?

    init(user: User, isYou: Bool) {
        self.user = user
        self.isYou = isYou
        _name = State(initialValue: user.name)
        _isAdmin = State(initialValue: user.isAdmin)
        _createProjects = State(initialValue: user.createProjects)
    }

    var body: some View {
        Form {
            Section {
                TextField("Name", text: $name)
                LabeledContent("Email", value: user.email)
            } footer: {
                Text("The email is the account's sign-in and cannot change.")
            }
            Section {
                Picker("Role", selection: $isAdmin) {
                    Text("Member").tag(false)
                    Text("Instance Admin").tag(true)
                }
                .disabled(isYou)
                if !isAdmin {
                    Toggle("Can Create Projects", isOn: $createProjects)
                }
            } footer: {
                if let error {
                    Text(error).foregroundStyle(.red)
                } else if isYou {
                    Text("Another admin has to change your own role.")
                }
            }
        }
        .navigationTitle("Edit User")
        .navigationBarTitleDisplayMode(.inline)
        .disabled(saving)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Cancel", role: .cancel) { dismiss() }
            }
            ToolbarItem(placement: .confirmationAction) {
                if saving {
                    ProgressView()
                } else {
                    Button("Save", action: save).disabled(patch.isEmpty)
                }
            }
        }
    }

    private var patch: UserPatch {
        UserPatch(
            from: user, name: name, role: isAdmin ? "admin" : "member",
            createProjects: createProjects, mayChangeRole: !isYou)
    }

    private func save() {
        saving = true
        error = nil
        Task {
            defer { saving = false }
            do {
                try await session.updateUser(id: user.id, patch)
                dismiss()
            } catch is CancellationError {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

/// Sets a new password for someone who lost theirs.
struct ResetPasswordView: View {
    let user: User

    @Environment(ClusterSession.self) private var session
    @Environment(\.dismiss) private var dismiss
    @State private var password = PasswordGenerator.generate()
    @State private var saving = false
    @State private var error: String?
    @State private var done = false

    var body: some View {
        if done {
            SignInDetailsView(user: user, password: password, action: .reset) { dismiss() }
        } else {
            Form {
                Section {
                    PasswordField(password: $password)
                } header: {
                    Text("New Password for \(displayName(user))")
                } footer: {
                    if let error {
                        Text(error).foregroundStyle(.red)
                    } else {
                        Text("Every session of the account is signed out.")
                    }
                }
            }
            .navigationTitle("Reset Password")
            .navigationBarTitleDisplayMode(.inline)
            .disabled(saving)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel", role: .cancel) { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    if saving {
                        ProgressView()
                    } else {
                        Button("Reset", action: save).disabled(password.count < 8)
                    }
                }
            }
        }
    }

    private func save() {
        saving = true
        error = nil
        Task {
            defer { saving = false }
            do {
                try await session.resetPassword(userID: user.id, newPassword: password)
                done = true
            } catch is CancellationError {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

/// A password typed for someone else: shown in the clear (it is handed
/// over, not remembered), never offered to the Passwords app, with a fresh
/// strong one a tap away.
struct PasswordField: View {
    @Binding var password: String

    var body: some View {
        HStack {
            TextField("Password", text: $password)
                .font(.body.monospaced())
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .textContentType(nil)
            Button("Generate", systemImage: "arrow.clockwise") {
                password = PasswordGenerator.generate()
            }
            .labelStyle(.iconOnly)
            .accessibilityLabel("Generate a Strong Password")
        }
    }
}

/// The details to hand over once an account exists or its password was
/// reset: shown once, to copy or share.
struct SignInDetailsView: View {
    enum Action { case created, reset }

    let user: User
    let password: String
    let action: Action
    let done: () -> Void

    @Environment(ClusterSession.self) private var session
    @State private var copied = false

    var body: some View {
        List {
            Section {
                VStack(spacing: 10) {
                    Image(systemName: action == .created ? "person.crop.circle.badge.checkmark" : "key.fill")
                        .font(.system(size: 44))
                        .foregroundStyle(.tint)
                    Text(action == .created ? "\(displayName(user)) Can Sign In" : "Password Reset")
                        .font(.title2.weight(.bold))
                        .multilineTextAlignment(.center)
                    Text(action == .created
                         ? "Share these details with them securely. This is the only time the password is shown."
                         : "All of their sessions were signed out. This is the only time the new password is shown.")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity)
            }
            .listRowBackground(Color.clear)

            Section {
                LabeledContent("Cluster") { Text(studioURL.host() ?? studioURL.absoluteString).monospaced() }
                LabeledContent("Email") { Text(user.email).textSelection(.enabled) }
                LabeledContent("Password") { Text(password).monospaced().textSelection(.enabled) }
            }

            Section {
                Button(copied ? "Copied" : "Copy Password", systemImage: copied ? "checkmark" : "doc.on.doc") {
                    UIPasteboard.general.string = password
                    copied = true
                }
                ShareLink(item: message) {
                    Label("Share Sign-In Details", systemImage: "square.and.arrow.up")
                }
            }
        }
        .navigationTitle(action == .created ? "New User" : "Reset Password")
        .navigationBarTitleDisplayMode(.inline)
        .navigationBarBackButtonHidden()
        .interactiveDismissDisabled()
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button("Done", action: done)
            }
        }
    }

    private var studioURL: URL { session.account.address.studioURL }

    private var message: String {
        """
        Sign in to Skali at \(studioURL.absoluteString)
        Email: \(user.email)
        Password: \(password)

        Change the password under Account after you sign in.
        """
    }
}
