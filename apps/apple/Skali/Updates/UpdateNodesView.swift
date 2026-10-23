import SkaliKit
import SwiftUI

/// The per-node picture, the Studio's update details panel as a sheet:
/// what every component runs on each node and, during an update, where
/// each node's step stands. Follows the screen's polling while open.
struct UpdateNodesView: View {
    /// A node to scroll to, e.g. the one whose step failed.
    let focusNode: String?

    @Environment(ClusterSession.self) private var session
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        ScrollViewReader { proxy in
            List {
                if let status = session.system.updates {
                    content(status)
                }
            }
            .task {
                // Once the list has laid out its rows.
                guard let focusNode else { return }
                await Task.yield()
                withAnimation { proxy.scrollTo(focusNode, anchor: .center) }
            }
        }
        .navigationTitle("Nodes")
        .navigationSubtitle(subtitle)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button("Done") { dismiss() }
            }
        }
    }

    private var subtitle: String {
        guard let status = session.system.updates else { return "" }
        if let operation = status.operation, operation.phase != "complete" {
            return [operation.phaseLabel, operation.targetVersion.map(displayVersion)]
                .compactMap(\.self).joined(separator: " · ")
        }
        return "Cluster release \(status.summary.convergedVersion.map(displayVersion) ?? "not verified")"
    }

    @ViewBuilder private func content(_ status: UpdateStatus) -> some View {
        let operation = status.operation
        let showSteps = operation.map { $0.phase != "complete" } ?? false

        if let error = operation?.error, showSteps {
            Section {
                Label {
                    Text(error).textSelection(.enabled)
                } icon: {
                    Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.red)
                }
            }
        }

        Section {
            LabeledContent("skalid and Studio") { Text(status.installed.version).monospaced() }
            LabeledContent("Cluster Release") {
                Text(status.summary.convergedVersion ?? "not verified").monospaced()
            }
        } header: {
            Text("Platform")
        } footer: {
            Text("The platform runs inside Kubernetes. A host agent runs on every node; controllers also run a coordinator.")
        }

        let controllers = status.nodes.filter(\.isController).sorted { $0.name < $1.name }
        let workers = status.nodes.filter { !$0.isController }.sorted { $0.name < $1.name }
        ForEach([("Controllers", controllers), ("Workers", workers)], id: \.0) { title, nodes in
            if !nodes.isEmpty {
                Section("\(title) · \(nodes.count)") {
                    ForEach(nodes) { node in
                        NodeVersionsRow(
                            node: node,
                            step: showSteps ? operation?.steps.first { $0.nodeId == node.id } : nil)
                        .id(node.id)
                        .listRowBackground(node.id == focusNode ? Color.red.opacity(0.1) : nil)
                    }
                }
            }
        }
    }
}

private struct NodeVersionsRow: View {
    let node: UpdateNode
    let step: UpdateOperation.Step?

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Text(node.name)
                    .font(.body.monospaced().weight(.semibold))
                    .lineLimit(1)
                Spacer(minLength: 4)
                if let step {
                    StepBadge(phase: step.phase)
                } else if node.phase != "active" {
                    TagChip(text: node.phase)
                }
            }
            Grid(alignment: .leading, horizontalSpacing: 12, verticalSpacing: 3) {
                versionRow("Host agent", node.agentVersion, seen: node.lastSeen)
                if node.isController {
                    versionRow("Coordinator", node.coordinatorVersion, seen: node.coordinatorLastSeen)
                }
                versionRow("Kubernetes", node.k3sVersion, seen: nil)
            }
            .font(.footnote)
            if let error = step?.error {
                Text(error)
                    .font(.footnote)
                    .foregroundStyle(.red)
                    .textSelection(.enabled)
            }
        }
        .padding(.vertical, 4)
    }

    private func versionRow(_ label: String, _ version: String?, seen: Date?) -> some View {
        GridRow {
            Text(label).foregroundStyle(.secondary)
            HStack(spacing: 4) {
                Text(version ?? "not reported").monospaced()
                if let seen {
                    Text("· \(compactAge(since: seen)) ago").foregroundStyle(.tertiary)
                }
            }
            .lineLimit(1)
        }
    }
}

/// Where one node's step stands during an update.
private struct StepBadge: View {
    let phase: UpdateOperation.Step.Phase

    var body: some View {
        Label {
            Text(label)
        } icon: {
            Circle().fill(color).frame(width: 7, height: 7)
        }
        .labelStyle(.titleAndIcon)
        .font(.caption.weight(.medium))
        .foregroundStyle(phase == .pending ? .secondary : color)
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
        .background(color.opacity(0.14), in: .capsule)
        .fixedSize()
    }

    private var label: String {
        switch phase {
        case .pending: "Waiting"
        case .running: "Upgrading"
        case .complete: "Upgraded"
        case .failed: "Failed"
        }
    }

    private var color: Color {
        switch phase {
        case .pending: .secondary
        case .running: .orange
        case .complete: .green
        case .failed: .red
        }
    }
}
