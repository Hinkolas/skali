import SkaliKit
import SwiftUI

/// Signs in to a cluster through its Studio in the browser, like
/// `skali remote add`: type the address, approve in the browser sheet.
struct SignInView: View {
    var onDone: (() -> Void)?

    @Environment(AppModel.self) private var model
    @AppStorage("signIn.lastAddress") private var addressText = ""
    @State private var userCode: String?
    @State private var signingIn = false
    @State private var error: String?
    @FocusState private var addressFocused: Bool

    var body: some View {
        Form {
            Section {
                VStack(spacing: 10) {
                    Image(systemName: "square.stack.3d.up.fill")
                        .font(.system(size: 48))
                        .foregroundStyle(.tint)
                    Text("Sign in to skali")
                        .font(.title.weight(.bold))
                    Text("Enter your cluster's address. You approve the sign-in in the browser, where you may already be signed in to the Studio.")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity)
                .listRowBackground(Color.clear)
            }

            Section {
                TextField("skali.example.com", text: $addressText)
                    .textContentType(.URL)
                    .keyboardType(.URL)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .submitLabel(.continue)
                    .focused($addressFocused)
                    .onSubmit(signIn)
                    .disabled(signingIn)
            } header: {
                Text("Cluster")
            } footer: {
                if let error {
                    Text(error).foregroundStyle(.red)
                }
            }

            if signingIn, let userCode {
                Section {
                    LabeledContent("Code", value: userCode)
                        .monospaced()
                    HStack {
                        ProgressView()
                        Text("Waiting for approval…").foregroundStyle(.secondary).padding(.leading, 6)
                    }
                } footer: {
                    Text("Check that the browser shows the same code before you approve.")
                }
            }
        }
        .safeAreaInset(edge: .bottom) {
            Button(action: signIn) {
                Text(signingIn ? "Waiting for Browser…" : "Continue in Browser")
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 6)
            }
            .buttonStyle(.glassProminent)
            .disabled(address == nil || signingIn)
            .padding()
        }
        .onAppear { addressFocused = addressText.isEmpty }
    }

    private var address: ClusterAddress? {
        ClusterAddress(userInput: addressText)
    }

    private func signIn() {
        guard let address, !signingIn else { return }
        error = nil
        signingIn = true
        Task {
            defer {
                signingIn = false
                userCode = nil
            }
            do {
                try await model.signIn(to: address) { userCode = $0 }
                onDone?()
            } catch is CancellationError {
                // The browser sheet was closed before approving.
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

#Preview {
    NavigationStack { SignInView() }
        .environment(AppModel(demo: false))
}
