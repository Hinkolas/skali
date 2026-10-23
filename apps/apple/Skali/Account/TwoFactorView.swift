import SkaliKit
import SwiftUI

/// Two-factor authentication: set it up, regenerate backup codes, or turn
/// it off. Every change waits for a recent sign-in, confirmed in the browser.
struct TwoFactorView: View {
    @Environment(ClusterSession.self) private var session
    @State private var enrollment: TwoFactorEnrollment?
    @State private var backupCodes: BackupCodes?
    @State private var confirmingDisable = false
    @State private var confirmingRegenerate = false
    @State private var working = false
    @State private var error: String?

    var body: some View {
        List {
            Section {
                Label {
                    Text(enabled ? "On" : "Off")
                } icon: {
                    Image(systemName: enabled ? "checkmark.shield.fill" : "shield.slash")
                        .foregroundStyle(enabled ? .green : .secondary)
                }
                .font(.headline)
            } footer: {
                Text(enabled
                     ? "Signing in asks for a code from your authenticator or a backup code."
                     : "Protect sign-in with a time-based code from the Passwords app or another authenticator.")
            }

            if enabled {
                Section {
                    Button("Regenerate Backup Codes") { confirmingRegenerate = true }
                } footer: {
                    Text("Your current backup codes stop working.")
                }
                Section {
                    Button("Turn Off Two-Factor Authentication", role: .destructive) { confirmingDisable = true }
                }
            } else {
                Section {
                    Button("Set Up Two-Factor Authentication") { run { enrollment = try await session.enableTwoFactor() } }
                }
            }
        }
        .navigationTitle("Two-Factor Authentication")
        .navigationBarTitleDisplayMode(.inline)
        .disabled(working)
        .overlay { if working { ProgressView() } }
        .confirmationDialog("Turn off two-factor authentication?", isPresented: $confirmingDisable, titleVisibility: .visible) {
            Button("Turn Off", role: .destructive) { run { try await session.disableTwoFactor() } }
        } message: {
            Text("Signing in falls back to your password alone, and your backup codes are deleted.")
        }
        .confirmationDialog("Regenerate backup codes?", isPresented: $confirmingRegenerate, titleVisibility: .visible) {
            Button("Regenerate") { run { backupCodes = BackupCodes(codes: try await session.regenerateBackupCodes()) } }
        } message: {
            Text("Your current backup codes stop working immediately.")
        }
        .alert("Something Went Wrong", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
            Button("OK") {}
        } message: {
            Text(error ?? "")
        }
        .sheet(item: $enrollment) { enrollment in
            NavigationStack { TwoFactorSetupView(enrollment: enrollment) }
                .interactiveDismissDisabled()
        }
        .sheet(item: $backupCodes) { backupCodes in
            NavigationStack {
                BackupCodesView(codes: backupCodes.codes)
                    .toolbar {
                        ToolbarItem(placement: .confirmationAction) {
                            Button("Done") { self.backupCodes = nil }
                        }
                    }
            }
            .interactiveDismissDisabled()
        }
    }

    private var enabled: Bool { session.user.twoFactorEnabled }

    private func run(_ action: @escaping () async throws -> Void) {
        working = true
        Task {
            defer { working = false }
            do {
                try await action()
            } catch is CancellationError {
                // The browser sheet was closed; nothing changed.
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

private struct BackupCodes: Identifiable {
    let id = UUID()
    let codes: [String]
}

extension TwoFactorEnrollment: @retroactive Identifiable {
    public var id: String { secret }
}

/// Adds the account to an authenticator and confirms it with a first code,
/// then shows the backup codes that became valid with it.
private struct TwoFactorSetupView: View {
    let enrollment: TwoFactorEnrollment

    @Environment(ClusterSession.self) private var session
    @Environment(\.dismiss) private var dismiss
    @Environment(\.openURL) private var openURL
    @State private var code = ""
    @State private var verifying = false
    @State private var confirmed = false
    @State private var error: String?
    @State private var noAuthenticator = false

    var body: some View {
        if confirmed {
            BackupCodesView(codes: enrollment.backupCodes)
                .toolbar {
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Done") { dismiss() }
                    }
                }
        } else {
            form
        }
    }

    private var form: some View {
        Form {
            Section {
                Button("Add to Passwords", systemImage: "key.viewfinder") {
                    guard let url = URL(string: enrollment.otpauthUri) else { return }
                    openURL(url) { accepted in noAuthenticator = !accepted }
                }
                LabeledContent("Setup Key") {
                    Text(groupedSecret)
                        .monospaced()
                        .textSelection(.enabled)
                }
            } header: {
                Text("Add the Account")
            } footer: {
                Text(noAuthenticator
                     ? "No app on this device opens setup links. Enter the setup key in your authenticator."
                     : "Opens the Passwords app, or the authenticator you use for codes. You can also enter the setup key by hand.")
            }

            Section {
                TextField("6-digit code", text: $code)
                    .keyboardType(.numberPad)
                    .textContentType(.oneTimeCode)
                    .monospacedDigit()
            } header: {
                Text("Enter a Code")
            } footer: {
                if let error {
                    Text(error).foregroundStyle(.red)
                } else {
                    Text("Enter the code your authenticator shows for skali to finish turning on two-factor authentication.")
                }
            }
        }
        .navigationTitle("Set Up Two-Factor")
        .navigationBarTitleDisplayMode(.inline)
        .disabled(verifying)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Cancel", role: .cancel) { dismiss() }
            }
            ToolbarItem(placement: .confirmationAction) {
                if verifying {
                    ProgressView()
                } else {
                    Button("Verify", action: verify).disabled(trimmedCode.count < 6)
                }
            }
        }
    }

    private var trimmedCode: String {
        code.filter(\.isNumber)
    }

    /// "JBSW Y3DP EHPK 3PXP": easier to type into another app.
    private var groupedSecret: String {
        stride(from: 0, to: enrollment.secret.count, by: 4).map { offset in
            let start = enrollment.secret.index(enrollment.secret.startIndex, offsetBy: offset)
            let end = enrollment.secret.index(start, offsetBy: 4, limitedBy: enrollment.secret.endIndex)
                ?? enrollment.secret.endIndex
            return String(enrollment.secret[start..<end])
        }
        .joined(separator: " ")
    }

    private func verify() {
        verifying = true
        error = nil
        Task {
            defer { verifying = false }
            do {
                try await session.confirmTwoFactor(code: trimmedCode)
                confirmed = true
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

/// One-time backup codes, shown once.
struct BackupCodesView: View {
    let codes: [String]

    var body: some View {
        List {
            Section {
                LazyVGrid(columns: [GridItem(.flexible()), GridItem(.flexible())], spacing: 12) {
                    ForEach(codes, id: \.self) { code in
                        Text(code).font(.body.monospaced())
                    }
                }
                .padding(.vertical, 8)
                .textSelection(.enabled)
            } footer: {
                Text("Each code signs you in once when your authenticator is not at hand. Keep them somewhere safe; they are not shown again.")
            }
            Section {
                Button("Copy Codes", systemImage: "doc.on.doc") {
                    UIPasteboard.general.string = codes.joined(separator: "\n")
                }
                ShareLink(item: codes.joined(separator: "\n")) {
                    Label("Share Codes", systemImage: "square.and.arrow.up")
                }
            }
        }
        .navigationTitle("Backup Codes")
        .navigationBarTitleDisplayMode(.inline)
    }
}
