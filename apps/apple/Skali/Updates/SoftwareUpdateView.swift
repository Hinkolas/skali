import SkaliKit
import SwiftUI

/// The cluster's software update: what runs, whether a newer release
/// exists, and an update's progress while one runs. The per-node picture
/// opens as a sheet from "Installed On".
struct SoftwareUpdateView: View {
    @Environment(ClusterSession.self) private var session
    @State private var nodesSheet: NodesSheet?
    @State private var busy = false
    @State private var confirmingUpdate = false
    @State private var error: String?

    var body: some View {
        List {
            if let status = session.system.updates {
                content(status)
            }
        }
        .overlay {
            if session.system.updates == nil { ProgressView() }
        }
        .navigationTitle("Software Update")
        .navigationBarTitleDisplayMode(.inline)
        .task { await poll() }
        .refreshable { await session.refreshUpdates() }
        .sheet(item: $nodesSheet) { sheet in
            NodesSheetContent(sheet: sheet)
        }
        .alert("Something Went Wrong", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
            Button("OK") {}
        } message: {
            Text(error ?? "")
        }
    }

    @ViewBuilder private func content(_ status: UpdateStatus) -> some View {
        Section {
            UpdateHero(status: status)
        }
        .listRowBackground(Color.clear)

        if session.updatesDisconnected {
            Section {
                Label {
                    Text("Reconnecting to the cluster. An accepted update continues in the background.")
                } icon: {
                    ProgressView()
                }
                .foregroundStyle(.secondary)
            }
        }

        if let lastError = status.lastError {
            Section {
                FeedErrorNotice(kind: status.lastErrorKind, detail: lastError)
            }
        }

        if status.summary.state == .updating || status.summary.state == .failed {
            UpdateProgressSection(status: status) { nodesSheet = NodesSheet(focusNode: $0) }
        }

        actionSection(status)

        if status.summary.state == .available, let latest = status.latest {
            ReleaseChangesSection(status: status, release: latest)
        }

        Section {
            Button {
                nodesSheet = NodesSheet(focusNode: nil)
            } label: {
                LabeledContent("Installed On") {
                    HStack(spacing: 6) {
                        Text(installedOn(status))
                        Image(systemName: "chevron.right")
                            .font(.footnote.weight(.semibold))
                            .foregroundStyle(.tertiary)
                    }
                }
            }
            .tint(.primary)
            LabeledContent("Kubernetes") {
                Text(UpdateStatus.tally(status.nodes.map(\.k3sVersion))).monospaced()
            }
            if let last = status.lastSuccessful, let version = last.targetVersion {
                LabeledContent("Last Update") {
                    Text("\(displayVersion(version)) · \((last.completedAt ?? last.updatedAt).formatted(.relative(presentation: .named)))")
                }
            }
        } footer: {
            if let url = status.installed.url.flatMap(URL.init(string:)) {
                Text("Every skali CLI that talks to this cluster fetches \(status.installed.version) on first contact. [Release page](\(url.absoluteString))")
            }
        }
    }

    /// One action per state, as the Studio has it; checking for updates
    /// whenever nothing else is to be done.
    @ViewBuilder private func actionSection(_ status: UpdateStatus) -> some View {
        let summary = status.summary
        switch summary.action {
        case .update, .finish, .retry:
            Section {
                Button {
                    if summary.action == .retry {
                        perform { try await session.resumeUpdate() }
                    } else {
                        confirmingUpdate = true
                    }
                } label: {
                    Group {
                        if busy {
                            ProgressView()
                        } else {
                            Text(summary.action.title)
                        }
                    }
                    .font(.headline)
                    .frame(maxWidth: .infinity, minHeight: 34)
                }
                .buttonStyle(.glassProminent)
                .disabled(busy || !status.canAct)
                .listRowInsets(EdgeInsets())
                .listRowBackground(Color.clear)
            } footer: {
                Text(actionFooter(status))
            }
            .confirmationDialog(
                "\(summary.action == .finish ? "Finish the update to" : "Update to") skali \(displayVersion(summary.targetVersion ?? ""))?",
                isPresented: $confirmingUpdate, titleVisibility: .visible
            ) {
                Button(summary.action.title) {
                    guard let target = summary.targetVersion else { return }
                    perform { try await session.applyUpdate(version: target) }
                }
            } message: {
                Text("Every node moves to the new release one at a time, controllers first, and the control plane restarts. Running applications keep serving.")
            }
        case .none where summary.state != .updating:
            Section {
                Button {
                    perform { try await session.scanForUpdates() }
                } label: {
                    HStack {
                        Spacer()
                        if busy { ProgressView() } else { Text("Check for Updates") }
                        Spacer()
                    }
                }
                .disabled(busy)
            } footer: {
                if !status.managed, let reason = status.reason ?? summary.detail {
                    Text(reason)
                }
            }
        default:
            EmptyView()
        }
    }

    private func actionFooter(_ status: UpdateStatus) -> String {
        if !status.canAct, let reason = status.reason { return reason }
        switch status.summary.action {
        case .retry: return "Continues with the steps that did not finish."
        case .finish: return "Brings every node to \(status.summary.targetVersion ?? "the release")."
        default: return "Nodes update one at a time, controllers first. Running applications keep serving."
        }
    }

    /// "11 of 11 nodes": nodes whose host agent runs the release the screen
    /// is about (the target while an update is under way).
    private func installedOn(_ status: UpdateStatus) -> String {
        guard !status.nodes.isEmpty else { return "No nodes reported" }
        let version = [.updating, .failed, .incomplete].contains(status.summary.state)
            ? status.summary.targetVersion ?? status.clusterVersion
            : status.clusterVersion
        let count = status.nodes.count { $0.agentVersion == version }
        return "\(count) of \(status.nodes.count) nodes"
    }

    /// Every three seconds while an update runs (the control plane restarts
    /// along the way), once a minute otherwise; only while on screen.
    private func poll() async {
        await session.refreshUpdates()
        while !Task.isCancelled {
            let running = session.system.updates?.summary.state == .updating
            try? await Task.sleep(for: .seconds(running ? 3 : 60))
            guard !Task.isCancelled else { return }
            await session.refreshUpdates()
        }
    }

    private func perform(_ action: @escaping () async throws -> Void) {
        busy = true
        Task {
            defer { busy = false }
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

private struct NodesSheet: Identifiable {
    let id = UUID()
    let focusNode: String?
}

/// Half height for a look at the nodes; full height when it opens on one
/// node, so the scroll to it has room.
private struct NodesSheetContent: View {
    let sheet: NodesSheet
    @State private var detent: PresentationDetent

    init(sheet: NodesSheet) {
        self.sheet = sheet
        _detent = State(initialValue: sheet.focusNode == nil ? .medium : .large)
    }

    var body: some View {
        NavigationStack { UpdateNodesView(focusNode: sheet.focusNode) }
            .presentationDetents([.medium, .large], selection: $detent)
            .presentationDragIndicator(.visible)
    }
}

extension UpdateStatus.Summary.Action {
    var title: String {
        switch self {
        case .update: "Update Now"
        case .finish: "Finish Update"
        case .retry: "Retry Update"
        case .none: ""
        }
    }
}

/// "v0.1.3-rc.8" as people say it: "0.1.3-rc.8".
func displayVersion(_ version: String) -> String {
    version.hasPrefix("v") ? String(version.dropFirst()) : version
}

#Preview("Up to date") {
    PreviewHost { SoftwareUpdateView() }
}

#Preview("Available") {
    PreviewHost(demo: DemoCluster(update: .available)) { SoftwareUpdateView() }
}

#Preview("Updating") {
    PreviewHost(demo: DemoCluster(update: .updating)) { SoftwareUpdateView() }
}

#Preview("Failed") {
    PreviewHost(demo: DemoCluster(update: .failed)) { SoftwareUpdateView() }
}
