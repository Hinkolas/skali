import SkaliKit
import SwiftUI

/// A session in the account's list: what signed in, and from where.
struct SessionRow: View {
    let item: Session

    var body: some View {
        let device = SessionDevice(userAgent: item.userAgent)
        HStack(spacing: 12) {
            SettingsIcon(systemImage: device.systemImage)
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(device.title)
                        .lineLimit(1)
                    if item.isCurrent {
                        Text("this device")
                            .font(.caption2.weight(.medium))
                            .foregroundStyle(.tint)
                            .padding(.horizontal, 6)
                            .padding(.vertical, 2)
                            .background(Color.accentColor.opacity(0.15), in: .capsule)
                            .fixedSize()
                    }
                }
                Text(detail(device))
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
        }
        .accessibilityElement(children: .combine)
    }

    private func detail(_ device: SessionDevice) -> String {
        let signedIn = item.createdAt.timeIntervalSinceNow > -60
            ? "just now" : "\(compactAge(since: item.createdAt)) ago"
        if item.isCurrent {
            return ["this \(device.host ?? UIDevice.current.model)", "signed in \(signedIn)"]
                .joined(separator: " · ")
        }
        return [
            device.host, item.ipAddress, signedIn,
            "expires \(item.expiresAt.formatted(.dateTime.month(.abbreviated).day()))",
        ]
        .compactMap { $0?.isEmpty == false ? $0 : nil }
        .joined(separator: " · ")
    }
}

/// Everything the cluster knows about one session, and signing it out.
struct SessionDetailView: View {
    let item: Session

    @Environment(ClusterSession.self) private var session
    @Environment(\.dismiss) private var dismiss
    @State private var confirming = false
    @State private var error: String?

    var body: some View {
        let device = SessionDevice(userAgent: item.userAgent)
        List {
            Section {
                LabeledContent("Device", value: device.title)
                if let host = device.host {
                    LabeledContent(device.kind == .cli ? "Host" : "Model", value: host)
                }
                LabeledContent("IP Address", value: item.ipAddress ?? "—")
                    .monospaced()
                LabeledContent("Signed In", value: item.createdAt, format: .dateTime)
                LabeledContent("Expires", value: item.expiresAt, format: .dateTime)
            } footer: {
                Text("A session stays signed in for 30 days after it was last used.")
            }
            if let userAgent = item.userAgent, !userAgent.isEmpty {
                Section("User Agent") {
                    Text(userAgent)
                        .font(.footnote.monospaced())
                        .textSelection(.enabled)
                }
            }
            if !item.isCurrent {
                Section {
                    Button("Sign Out This Session", role: .destructive) { confirming = true }
                        .frame(maxWidth: .infinity)
                        .confirmationDialog("Sign out this session?", isPresented: $confirming, titleVisibility: .visible) {
                            Button("Sign Out", role: .destructive) { revoke() }
                        } message: {
                            Text("\(device.title) is signed out immediately.")
                        }
                }
            }
        }
        .navigationTitle(device.title)
        .navigationBarTitleDisplayMode(.inline)
        .alert("Could Not Sign Out Session", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
            Button("OK") {}
        } message: {
            Text(error ?? "")
        }
    }

    private func revoke() {
        Task {
            do {
                try await session.revoke(item)
                dismiss()
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

extension SessionDevice {
    var systemImage: String {
        switch kind {
        case .cli: "apple.terminal"
        case .app, .browser, .unknown:
            isTablet ? "ipad" : isMobile ? "iphone" : kind == .unknown ? "network" : "desktopcomputer"
        }
    }
}
