import SkaliKit
import SwiftUI

/// An application: how it is doing and what it uses now, where it answers,
/// the pods that run it and their logs, what changed it lately, how it is
/// built and started, and what it talks to and stores.
struct ApplicationServiceView: View {
    let key: String
    let spec: ApplicationSpec
    let model: ProjectModel

    @Environment(\.openURL) private var openURL
    @State private var confirmingRestart = false
    @State private var startedRun: String?
    @State private var showingLogs = false
    @State private var error: String?

    var body: some View {
        EnvironmentGate(model: model) { environment in
            List {
                Section {
                    ServiceHeaderView(
                        kind: .application, name: key, state: model.liveState(.application, key),
                        detail: "application · \(spec.sourceLabel)", dnsPending: (live?.deferredRoutes ?? 0) > 0)
                }
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets(top: 0, leading: 4, bottom: 8, trailing: 4))

                diagnosticsSection

                Section {
                    UsageCarousel(stats: stats, link: ApplicationUsageLink(key: key))
                        .redacted(reason: model.metrics == nil ? .placeholder : [])
                        .listRowInsets(EdgeInsets())
                        .listRowBackground(Color.clear)
                } header: {
                    Text("Last 24 Hours")
                }

                RoutesSection(routes: live?.routes ?? [])
                podsSection
                Section {
                    NavigationLink(value: RuntimeLogsLink(key: key)) {
                        Label { Text("Live Logs") } icon: { SettingsIcon(systemImage: "text.alignleft", tint: .green) }
                    }
                } footer: {
                    Text("Follows every pod's output as it is written.")
                }
                changesSection
                runtimeSection
                connectedSection
                storageSection
            }
        }
        .toolbar { menu }
        .navigationDestination(item: $startedRun) { RunDetailView(runID: $0, model: model) }
        .navigationDestination(for: ApplicationUsageLink.self) { _ in
            ApplicationUsageView(key: key, spec: spec, model: model)
        }
        .navigationDestination(for: RuntimeLogsLink.self) { _ in RuntimeLogsView(key: key, model: model) }
        .navigationDestination(isPresented: $showingLogs) { RuntimeLogsView(key: key, model: model) }
        .alert("Could Not Restart", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
            Button("OK") {}
        } message: {
            Text(error ?? "")
        }
    }

    private var live: ServiceStatus? { model.liveStatus(.application, key) }

    private var stats: [UsageStat] {
        guard let metrics = model.metrics else {
            return [UsageStat.Kind.requests, .cpu, .memory, .traffic].map(UsageStat.empty)
        }
        return metrics.applicationStats(key: key, spec: spec, status: live)
    }

    // MARK: Sections

    @ViewBuilder private var diagnosticsSection: some View {
        let diagnostics = live?.diagnostics ?? []
        if !diagnostics.isEmpty {
            Section {
                ForEach(Array(diagnostics.enumerated()), id: \.offset) { _, diagnostic in
                    Label {
                        Text(diagnostic.message)
                    } icon: {
                        Image(systemName: diagnostic.severity == "error" ? "xmark.octagon.fill" : "exclamationmark.triangle.fill")
                            .foregroundStyle(diagnostic.severity == "error" ? .red : .orange)
                    }
                }
            }
        }
    }

    @ViewBuilder private var podsSection: some View {
        let pods = (live?.pods ?? []).sorted { a, b in
            (a.serving ?? true) != (b.serving ?? true) ? (a.serving ?? true) : a.name < b.name
        }
        Section {
            if pods.isEmpty {
                Text(model.status == nil ? "Waiting for the cluster's status." : "No pods are running right now.")
                    .foregroundStyle(.secondary)
            } else {
                ForEach(pods, id: \.name) { pod in
                    PodRow(pod: pod)
                }
            }
        } header: {
            HStack {
                Text("Pods")
                Spacer()
                if live != nil {
                    Text("\(pods.filter(\.ready).count)/\(pods.count) ready · scale \(spec.scaleLabel)")
                        .font(.caption.monospaced())
                        .textCase(nil)
                }
            }
        }
    }

    @ViewBuilder private var changesSection: some View {
        let changes = Array((model.runs ?? []).changes(to: "applications.\(key)").prefix(5))
        if !changes.isEmpty {
            Section("Recent Changes") {
                ForEach(changes, id: \.run.id) { change in
                    NavigationLink(value: ProjectRoute.run(change.run.id)) {
                        ServiceChangeRow(change: change, directory: model.directory)
                    }
                }
            }
        }
    }

    private var runtimeSection: some View {
        Section("Runtime") {
            LabeledContent("Source") {
                Text(spec.sourceLabel).monospaced().lineLimit(2).truncationMode(.middle)
            }
            LabeledContent("Start Command") {
                Text(spec.command?.joined(separator: " ") ?? "image default")
                    .monospaced()
                    .foregroundStyle(spec.command == nil ? .secondary : .primary)
                    .lineLimit(3)
            }
            LabeledContent("Ports") { Text(spec.portsLabel ?? "none").monospaced() }
            LabeledContent("Health Check") { Text(spec.healthCheckLabel ?? "none").monospaced() }
            LabeledContent("Rollout") { Text(spec.rolloutStrategy).monospaced() }
            if let limits = spec.resources?.limits, limits.milliCpu != nil || limits.memoryBytes != nil {
                LabeledContent("Limits per Pod") {
                    Text([limits.milliCpu.map { "\(formatCores($0)) CPU" }, limits.memoryBytes.map(formatBytes)]
                        .compactMap(\.self).joined(separator: " · "))
                        .monospaced()
                }
            }
        }
    }

    @ViewBuilder private var connectedSection: some View {
        let dependencies = model.definition?.dependencies(of: key) ?? []
        if !dependencies.isEmpty {
            Section("Connected Services") {
                ForEach(dependencies, id: \.key) { dependency in
                    NavigationLink(value: ProjectRoute.service(dependency.kind, dependency.key)) {
                        ConnectedServiceRow(
                            kind: dependency.kind, name: dependency.key,
                            note: dependency.kind == .database ? "env injected" : "s3 keys injected")
                    }
                }
            }
        }
    }

    @ViewBuilder private var storageSection: some View {
        let samples = model.storage(of: .application, key)
        if !samples.isEmpty {
            Section("Storage") {
                ForEach(samples, id: \.kind) { sample in
                    StorageServiceRow(service: sample)
                }
            }
        }
    }

    // MARK: Menu

    @ToolbarContentBuilder private var menu: some ToolbarContent {
        ToolbarItem(placement: .primaryAction) {
            Menu {
                Section {
                    if let url = live?.routes?.first?.url {
                        Button("Open App", systemImage: "arrow.up.forward.app") { openURL(url) }
                    }
                    Button("Live Logs", systemImage: "text.alignleft") { showingLogs = true }
                }
                Section {
                    Button("Restart", systemImage: "arrow.clockwise") { confirmingRestart = true }
                        .disabled(!canRestart)
                }
                Section {
                    Button("Open in Studio", systemImage: "safari") { openURL(model.studioURL("services/\(key)")) }
                }
            } label: {
                Label("More", systemImage: "ellipsis")
            }
            .disabled(!model.environmentIsReadable)
            .confirmationDialog("Restart \(key)?", isPresented: $confirmingRestart, titleVisibility: .visible) {
                Button("Restart") { restart() }
            } message: {
                Text("Recreates its pods with the same revision, values and image. The rollout follows its strategy; stored data is untouched.")
            }
        }
    }

    private var canRestart: Bool {
        guard let environment = model.environment, model.environmentIsReadable else { return false }
        return environment.role >= .deploy && environment.state == "active" && live != nil
    }

    private func restart() {
        Task {
            do {
                startedRun = try await model.restartApplication(key)
            } catch is CancellationError {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

/// Opens an application's usage history.
struct ApplicationUsageLink: Hashable {
    let key: String
}

/// Opens an application's live logs.
struct RuntimeLogsLink: Hashable {
    let key: String
}

/// One pod: whether it is ready, its node, restarts and age, and on
/// blue-green rollouts its color and whether it takes traffic.
private struct PodRow: View {
    let pod: PodStatus

    var body: some View {
        HStack(spacing: 12) {
            Circle()
                .fill(pod.ready ? .green : pod.isFailing ? .red : .orange)
                .frame(width: 8, height: 8)
                .accessibilityLabel(pod.stateLabel)
            VStack(alignment: .leading, spacing: 3) {
                Text(pod.name)
                    .font(.subheadline.monospaced())
                    .lineLimit(1)
                    .truncationMode(.middle)
                HStack(spacing: 0) {
                    Text(pod.node)
                    Text(" · ")
                    Text(pod.restarts == 1 ? "1 restart" : "\(pod.restarts) restarts")
                        .foregroundStyle(pod.restarts > 0 ? .orange : .secondary)
                    if let started = pod.startedAt {
                        Text(" · \(compactAge(since: started))")
                    }
                    if !pod.ready {
                        Text(" · \(pod.stateLabel)")
                            .foregroundStyle(pod.isFailing ? .red : .orange)
                    }
                }
                .font(.caption.monospaced())
                .foregroundStyle(.secondary)
                .lineLimit(2)
            }
            Spacer(minLength: 8)
            if let color = pod.color {
                // The hash's first block tells the colors apart.
                TagChip(text: String(color.prefix(6)), tint: pod.serving == false ? nil : .green)
            } else if pod.serving == false {
                TagChip(text: "idle")
            }
        }
        .accessibilityElement(children: .combine)
    }
}

/// A run that changed the service: what it did, by whom and when.
private struct ServiceChangeRow: View {
    let change: ServiceChange
    let directory: [String: DirectoryUser]

    var body: some View {
        HStack(spacing: 12) {
            RunStatusTile(status: change.run.status)
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(actionWord(change.action).capitalized)
                        .font(.headline)
                        .foregroundStyle(change.destructive ? .red : .primary)
                    Text(change.run.kindLabel)
                        .font(.caption.monospaced())
                        .foregroundStyle(.secondary)
                }
                Text([change.detail, "\(describeActor(change.run.actor, directory: directory)) · \(compactAge(since: change.run.createdAt)) ago"]
                    .compactMap(\.self).joined(separator: " · "))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
        }
        .padding(.vertical, 2)
    }
}
