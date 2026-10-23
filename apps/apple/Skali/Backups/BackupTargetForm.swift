import SkaliKit
import SwiftUI

/// Configures the backup target, or changes the stored one. The whole
/// target is replaced, so the secret is asked for every time: it is
/// write-only and never comes back to fill in.
struct BackupTargetForm: View {
    let target: BackupTarget?

    @Environment(ClusterSession.self) private var session
    @Environment(\.dismiss) private var dismiss
    @State private var input: BackupTargetInput
    @State private var saving = false
    @State private var error: String?

    init(target: BackupTarget?) {
        self.target = target
        _input = State(initialValue: target.map(BackupTargetInput.init) ?? BackupTargetInput())
    }

    var body: some View {
        Form {
            Section {
                TextField("https://s3.example.com", text: $input.endpoint)
                    .keyboardType(.URL)
                    .textContentType(.URL)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                field("Region", text: $input.region, prompt: "Optional, e.g. eu-central-1")
            } header: {
                Text("Endpoint")
            } footer: {
                Text("Any S3-compatible service: AWS, Cloudflare R2, Backblaze B2, MinIO, Hetzner.")
            }

            Section {
                field("Bucket", text: $input.bucket, prompt: "skali-backup")
                field("Prefix", text: $input.prefix, prompt: "Optional")
            } header: {
                Text("Location")
            } footer: {
                Text("The bucket must exist. A prefix keeps several installations apart in one bucket.")
            }

            Section {
                field("Access Key ID", text: $input.accessKeyId, prompt: "Required")
                LabeledContent("Secret Key") {
                    // An API key pasted from the storage provider, not the
                    // person's password: a secure field next to the key ID
                    // reads as a login form, and iOS offers to save it in
                    // Passwords. Kept out of screenshots and the app switcher.
                    TextField(target == nil ? "Required" : "Enter it again", text: $input.secretAccessKey)
                        .multilineTextAlignment(.trailing)
                        .font(.body.monospaced())
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .privacySensitive()
                }
            } header: {
                Text("Credentials")
            } footer: {
                if let error {
                    Text(error).foregroundStyle(.red)
                } else {
                    Text("The secret is stored encrypted and never shown again, so saving a change asks for it each time.")
                }
            }
        }
        .navigationTitle(target == nil ? "Backup Target" : "Edit Target")
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
                    Button("Save", action: save).disabled(!input.isComplete)
                }
            }
        }
    }

    private func field(_ label: String, text: Binding<String>, prompt: String) -> some View {
        LabeledContent(label) {
            TextField(prompt, text: text)
                .multilineTextAlignment(.trailing)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
        }
    }

    private func save() {
        saving = true
        error = nil
        Task {
            defer { saving = false }
            var trimmed = input
            trimmed.endpoint = input.endpoint.trimmingCharacters(in: .whitespaces)
            trimmed.bucket = input.bucket.trimmingCharacters(in: .whitespaces)
            trimmed.accessKeyId = input.accessKeyId.trimmingCharacters(in: .whitespaces)
            do {
                try await session.saveBackupTarget(trimmed)
                dismiss()
            } catch is CancellationError {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}
