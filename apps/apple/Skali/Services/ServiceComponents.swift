import SkaliKit
import SwiftUI

/// One service of the project, by kind: the page the overview's service
/// rows open.
struct ServicePage: View {
    let kind: ServiceKind
    let key: String
    let model: ProjectModel

    var body: some View {
        Group {
            if let definition = model.definition {
                switch kind {
                case .application:
                    if let spec = definition.applications?[key] {
                        ApplicationServiceView(key: key, spec: spec, model: model)
                    } else {
                        missing
                    }
                case .database:
                    if let spec = definition.databases?[key] {
                        DatabaseServiceView(key: key, spec: spec, model: model)
                    } else {
                        missing
                    }
                case .bucket:
                    if let spec = definition.buckets?[key] {
                        BucketServiceView(key: key, spec: spec, model: model)
                    } else {
                        missing
                    }
                }
            } else {
                ProgressView()
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .navigationTitle(key)
        .navigationBarTitleDisplayMode(.inline)
        .projectChrome(model, followsStatus: true)
    }

    private var missing: some View {
        ContentUnavailableView(
            "Service Not Found", systemImage: kind.systemImage,
            description: Text("\(key) is no longer part of the project's definition."))
    }
}

/// The top of a service page: its kind's tile, its name with its health,
/// and a line on what it is.
struct ServiceHeaderView: View {
    let kind: ServiceKind
    let name: String
    let state: ServiceLiveState
    let detail: String
    /// A route's domain does not reach the cluster yet.
    var dnsPending = false

    @ScaledMetric(relativeTo: .largeTitle) private var tile: CGFloat = 58

    var body: some View {
        HStack(spacing: 16) {
            ResourceTile(systemImage: kind.systemImage, tint: kind.tint, size: tile)
            VStack(alignment: .leading, spacing: 6) {
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: 10) {
                        title
                        capsules
                    }
                    VStack(alignment: .leading, spacing: 6) {
                        title
                        HStack(spacing: 8) { capsules }
                    }
                }
                Text(detail)
                    .font(.subheadline.monospaced())
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
    }

    private var title: some View {
        Text(name)
            .font(.largeTitle.bold())
            .lineLimit(1)
            .minimumScaleFactor(0.6)
    }

    @ViewBuilder private var capsules: some View {
        switch state {
        case .pending:
            ProgressView().controlSize(.small)
        case .notDeployed:
            StatusCapsule(text: "Not Deployed", tint: .secondary)
        case .health(let health):
            StatusCapsule(text: health.label, tint: health.tint)
        }
        if dnsPending {
            StatusCapsule(text: "DNS Pending", tint: .orange)
        }
    }
}

extension ProjectModel {
    /// Where a service stands in the environment on screen.
    func liveState(_ kind: ServiceKind, _ key: String) -> ServiceLiveState {
        guard let status else { return .pending }
        return status.service(kind, key).map { .health($0.health) } ?? .notDeployed
    }
}

/// How full a database or bucket is against what it asked for.
struct CapacityCard: View {
    let title: String
    let used: Int64?
    let capacity: Int64?
    /// "quota" for a bucket's limit; a database's size reads plain.
    var capacityWord: String?
    let tint: Color

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(title.uppercased())
                .font(.caption2.monospaced().weight(.medium))
                .foregroundStyle(.secondary)
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(used.map(formatBytes) ?? (capacity.map(formatBytes) ?? "—"))
                    .font(.largeTitle.weight(.bold))
                    .contentTransition(.numericText())
                Text(caption)
                    .foregroundStyle(.secondary)
            }
            .lineLimit(1)
            .minimumScaleFactor(0.7)
            if let used, let capacity, capacity > 0 {
                UsageBar(fraction: min(1, Double(used) / Double(capacity)), color: tint, height: 6)
            }
        }
        .padding(.vertical, 6)
        .accessibilityElement(children: .combine)
    }

    private var caption: String {
        guard used != nil else { return capacity == nil ? "not measured yet" : "requested, not measured yet" }
        guard let capacity, capacity > 0 else { return "no limit" }
        return ["of", formatBytes(capacity), capacityWord].compactMap(\.self).joined(separator: " ")
    }
}

/// A service another one is connected to, with how the connection reaches
/// it.
struct ConnectedServiceRow: View {
    let kind: ServiceKind
    let name: String
    let note: String

    @ScaledMetric(relativeTo: .body) private var tile: CGFloat = 32

    var body: some View {
        HStack(spacing: 12) {
            ResourceTile(systemImage: kind.systemImage, tint: kind.tint, size: tile)
            Text(name)
            Spacer(minLength: 8)
            Text(note)
                .font(.caption.monospaced())
                .foregroundStyle(.secondary)
        }
    }
}

/// The way to the project's backups from a stateful service: the
/// environment's schedule, or that nothing backs it up.
struct ServiceBackupsRow: View {
    let environment: ProjectEnvironment
    let runs: [Run]?

    var body: some View {
        NavigationLink(value: ProjectRoute.backups) {
            LabeledContent {
                if let schedule = environment.settings?.backup {
                    if let last = runs?.lastSucceededBackup {
                        Text("\(schedule.cadence) · last \(compactAge(since: last.finishedAt ?? last.createdAt)) ago")
                    } else {
                        Text(schedule.cadence)
                    }
                } else {
                    TagChip(text: "not backed up", tint: .orange)
                }
            } label: {
                Label { Text("Backups") } icon: { SettingsIcon(systemImage: "archivebox", tint: .orange) }
            }
        }
    }
}

/// The public routes of an application or bucket: where they answer, and
/// their certificate or why it waits. Each opens in the browser.
struct RoutesSection: View {
    let routes: [RouteStatus]

    var body: some View {
        if !routes.isEmpty {
            Section("Routes") {
                ForEach(routes, id: \.key) { route in
                    RouteRow(domain: route.domain, url: route.url, certificate: route.certificate,
                             deferred: route.edge?.deferred == true, note: note(route))
                    ForEach(route.aliases ?? [], id: \.domain) { alias in
                        RouteRow(domain: alias.domain, url: URL(string: "https://\(alias.domain)"),
                                 certificate: alias.certificate, deferred: alias.edge?.deferred == true,
                                 note: "redirects to \(route.domain)")
                            .padding(.leading, 18)
                    }
                }
            }
        }
    }

    private func note(_ route: RouteStatus) -> String? {
        var parts: [String] = []
        if let path = route.path, path != "/" { parts.append(path) }
        if route.strategy == "least-requests" { parts.append("least requests") }
        if route.tls == "optional" { parts.append("http allowed") }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}

private struct RouteRow: View {
    let domain: String
    let url: URL?
    let certificate: CertificateStatus?
    let deferred: Bool
    let note: String?

    @Environment(\.openURL) private var openURL

    var body: some View {
        Button {
            if let url { openURL(url) }
        } label: {
            HStack(spacing: 10) {
                VStack(alignment: .leading, spacing: 4) {
                    Text(domain.wrappingAtSeams)
                        .font(.subheadline.monospaced())
                        .typesetAsCode()
                        .lineLimit(2)
                    HStack(spacing: 8) {
                        state
                        if let note {
                            Text(note)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                    }
                }
                Spacer(minLength: 8)
                Image(systemName: "arrow.up.right")
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(.tertiary)
            }
        }
        .tint(.primary)
        .disabled(url == nil)
    }

    @ViewBuilder private var state: some View {
        if deferred {
            StateDot(text: "DNS pending", tint: .orange)
        } else if let certificate {
            StateDot(text: certificateLabel(certificate.state), tint: certificateTint(certificate.state))
        }
    }

    private func certificateLabel(_ state: String) -> String {
        switch state {
        case "active": "TLS active"
        case "issuing", "pending": "TLS issuing"
        case "failing": "TLS failing"
        case "expired": "TLS expired"
        default: state
        }
    }

    private func certificateTint(_ state: String) -> Color {
        switch state {
        case "active": .green
        case "issuing", "pending": .orange
        default: .red
        }
    }
}

/// "● TLS active": a small state in its color.
struct StateDot: View {
    let text: String
    let tint: Color

    var body: some View {
        HStack(spacing: 5) {
            Circle().fill(tint).frame(width: 7, height: 7)
            Text(text)
        }
        .font(.caption)
        .foregroundStyle(tint)
        .fixedSize()
    }
}

extension View {
    /// The menu of a database or bucket: snapshotting the environment it
    /// lives in, and the service in the Studio.
    func statefulServiceMenu(_ model: ProjectModel, key: String) -> some View {
        modifier(StatefulServiceMenu(model: model, key: key))
    }
}

private struct StatefulServiceMenu: ViewModifier {
    let model: ProjectModel
    let key: String

    @Environment(\.openURL) private var openURL
    @State private var confirming = false
    @State private var startedRun: String?
    @State private var error: String?

    func body(content: Content) -> some View {
        content
            .toolbar {
                ToolbarItem(placement: .primaryAction) {
                    Menu {
                        Button {
                            confirming = true
                        } label: {
                            Label("Back Up Now", systemImage: "arrow.up.doc")
                            if let refusal = model.backupRefusal { Text(refusal) }
                        }
                        .disabled(model.backupRefusal != nil)
                        Button("Open in Studio", systemImage: "safari") { openURL(model.studioURL("services/\(key)")) }
                    } label: {
                        Label("More", systemImage: "ellipsis")
                    }
                    .disabled(!model.environmentIsReadable)
                    .confirmationDialog(
                        "Back up \(model.environment?.name ?? "") now?", isPresented: $confirming, titleVisibility: .visible
                    ) {
                        Button("Back Up") { backUp() }
                    } message: {
                        Text("A snapshot covers every database, bucket and volume of the environment, \(key) included. Manual snapshots are kept until you delete them.")
                    }
                }
            }
            .navigationDestination(item: $startedRun) { RunDetailView(runID: $0, model: model) }
            .alert("Could Not Back Up", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
                Button("OK") {}
            } message: {
                Text(error ?? "")
            }
    }

    private func backUp() {
        Task {
            do {
                startedRun = try await model.startBackup()
            } catch is CancellationError {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}
