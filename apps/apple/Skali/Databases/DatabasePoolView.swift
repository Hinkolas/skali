import SkaliKit
import SwiftUI

/// One database pool, read only: its health, what it uses now, its usage
/// over time, how it is set up, its instances and the databases on it.
struct DatabasePoolView: View {
    let name: String

    @Environment(ClusterSession.self) private var session
    @State private var window: NodeMetrics.Window = .day
    @State private var metric: PoolMetric = .connections
    @State private var history: DatabasePoolMetrics?
    @State private var showingDatabases = false

    /// The databases the screen lists before "Show All Databases".
    private let databasePreview = 3

    var body: some View {
        List {
            if let pool {
                Section {
                    ResourceHeader(
                        systemImage: "cylinder.split.1x2", tint: pool.status.tileTint, name: pool.name,
                        status: StatusCapsule(text: pool.status.label, tint: pool.status.tint),
                        detail: "\(pool.class) · \(pool.engineLabel)")
                }
                .listRowBackground(Color.clear)

                if let connections = pool.connections, connections.state != .available {
                    ConnectionNotice(connections: connections)
                }

                Section {
                    usageTiles(pool)
                }
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets())

                usageSection
                if let detail { databasesSection(detail) }
                poolSection(pool)
                if let detail { instancesSection(detail) }
                if let detail, !detail.incidents.isEmpty { incidentsSection(detail.incidents) }
            }
        }
        .overlay {
            if session.system.pools == nil && detail == nil {
                ProgressView()
            } else if pool == nil {
                ContentUnavailableView("Pool Not Found", systemImage: "cylinder",
                                       description: Text("\(name) no longer exists."))
            }
        }
        .navigationTitle(name)
        .navigationBarTitleDisplayMode(.inline)
        .task {
            while !Task.isCancelled {
                await session.loadPool(name)
                try? await Task.sleep(for: .seconds(30))
            }
        }
        .task(id: window) { await loadHistory() }
        .refreshable {
            await session.loadPool(name)
            await loadHistory()
        }
        .sheet(isPresented: $showingDatabases) {
            if let detail {
                NavigationStack { PoolDatabasesView(detail: detail) }
                    .presentationDetents([.medium, .large])
                    .presentationDragIndicator(.visible)
            }
        }
    }

    private var detail: DatabasePoolDetail? { session.poolDetails[name] }

    /// The freshest pool facts: the detail's when loaded, else the list's.
    private var pool: DatabasePool? {
        detail?.pool ?? session.system.pools?.first { $0.name == name }
    }

    // MARK: Now

    private func usageTiles(_ pool: DatabasePool) -> some View {
        let current = session.poolUsage[name]
        let budget = current?.memoryBudgetBytes ?? pool.memory.bytes
        let memoryFraction = current.flatMap { current in
            budget.map { min(1, Double(current.memoryBytes) / Double(max(1, $0))) }
        }
        let storageFraction = current?.databaseBytes.map { min(1, Double($0) / Double(max(1, pool.storageBytes))) }
        let used = pool.connections?.used ?? current?.connections
        let limit = pool.connections?.limit ?? current?.maxConnections
        return UsageTileRow {
            UsageTile(
                title: "Connections", value: used.map(String.init) ?? "—",
                fraction: pool.connectionFraction, color: pool.connectionColor,
                caption: limit.map { "of \($0)" } ?? "unchecked")
            UsageTile(
                title: "Memory", value: UsageTile.percent(memoryFraction), fraction: memoryFraction,
                color: .usageMemory, caption: budget.map { "of \(formatBytes($0))" } ?? "no budget")
            UsageTile(
                title: "Storage", value: UsageTile.percent(storageFraction), fraction: storageFraction,
                color: .usageDisk, caption: "of \(formatBytes(pool.storageBytes))")
        }
    }

    // MARK: Over time

    private var usageSection: some View {
        Section {
            MetricWindowPicker(window: $window)
                .listRowSeparator(.hidden)
            MetricChart(
                series: history.map { metric.series($0) } ?? [], window: window,
                yDomain: metric.yDomain, format: metric.format)
            .frame(height: 180)
            .padding(.vertical, 4)
        } header: {
            HStack {
                Text("Usage")
                Spacer()
                Picker("Metric", selection: $metric) {
                    ForEach(PoolMetric.allCases) { metric in
                        Text(metric.title).tag(metric)
                    }
                }
                .pickerStyle(.menu)
                .labelsHidden()
                .textCase(nil)
                .font(.subheadline)
            }
        } footer: {
            Text(metric.footnote(stepSeconds: history?.stepSeconds))
        }
    }

    private func loadHistory() async {
        if let metrics = try? await session.api.databasePoolMetrics(name: name, window: window) {
            history = metrics
        }
    }

    // MARK: Databases

    private func databasesSection(_ detail: DatabasePoolDetail) -> some View {
        let databases = detail.databasesBySize
        return Section {
            if databases.isEmpty {
                Text("The first declared database on this pool lands here.")
                    .foregroundStyle(.secondary)
            }
            ForEach(databases.prefix(databasePreview)) { database in
                PoolDatabaseRow(database: database)
            }
            if databases.count > databasePreview {
                Button("Show All \(databases.count) Databases") { showingDatabases = true }
            }
        } header: {
            Text("Databases")
        } footer: {
            if !databases.isEmpty {
                Text(databases.count > databasePreview ? "The largest by logical size." : "By logical size.")
            }
        }
    }

    // MARK: Setup

    private func poolSection(_ pool: DatabasePool) -> some View {
        Section("Pool") {
            LabeledContent("Class", value: pool.classDescription)
            LabeledContent("Engine") { Text(pool.engineLabel).monospaced() }
            if !pool.image.isEmpty {
                LabeledContent("Image") {
                    Text(pool.image).monospaced().lineLimit(1).truncationMode(.middle).textSelection(.enabled)
                }
            }
            LabeledContent("Instances", value: instancesText(pool))
            LabeledContent("Memory Budget", value: pool.budgetText)
            LabeledContent("Storage", value: "\(formatBytes(pool.storageBytes)) per instance")
            if let port = pool.nodePort {
                LabeledContent("Node Port") { Text(String(port)).monospaced() }
            }
            LabeledContent("Created", value: pool.createdAt, format: .dateTime.day().month().year())
        }
    }

    private func instancesText(_ pool: DatabasePool) -> String {
        guard let observed = pool.observed else { return "\(pool.instances) declared · not observed" }
        return "\(observed.readyInstances) of \(pool.instances) ready"
    }

    private func instancesSection(_ detail: DatabasePoolDetail) -> some View {
        Section {
            ForEach(detail.members) { member in
                MemberRow(member: member)
            }
        } header: {
            Text("Instances · \(detail.memberSummary)")
        }
    }

    private func incidentsSection(_ incidents: [DatabasePool.Incident]) -> some View {
        Section {
            ForEach(incidents) { incident in
                VStack(alignment: .leading, spacing: 4) {
                    HStack {
                        Text(incident.openedAt, format: .dateTime.day().month().hour().minute())
                            .font(.subheadline.monospacedDigit())
                        Spacer()
                        if let closed = incident.closedAt {
                            Text("Recovered after \(Duration.seconds(closed.timeIntervalSince(incident.openedAt)).formatted(.units(allowed: [.hours, .minutes], width: .abbreviated)))")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        } else {
                            StatusCapsule(text: "Ongoing", tint: .red)
                        }
                    }
                    Text(incident.message)
                        .font(.caption.monospaced())
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                }
                .padding(.vertical, 2)
            }
        } header: {
            Text("Connection Incidents")
        } footer: {
            Text("Times the pool turned new connections away, newest first.")
        }
    }
}

/// A pool that needs attention: what is wrong with connections, since when.
private struct ConnectionNotice: View {
    let connections: DatabasePool.Connections

    var body: some View {
        Section {
            Label {
                VStack(alignment: .leading, spacing: 4) {
                    Text(title).font(.headline)
                    if let since = connections.since {
                        Text("Since \(since.formatted(.relative(presentation: .named)))")
                            .font(.subheadline)
                            .foregroundStyle(.secondary)
                    }
                    if let reason = connections.reason {
                        Text(reason).font(.caption.monospaced()).foregroundStyle(.secondary)
                    }
                    Text(advice).font(.subheadline).foregroundStyle(.secondary)
                }
            } icon: {
                Image(systemName: connections.state == .unknown ? "questionmark.circle" : "exclamationmark.triangle.fill")
                    .foregroundStyle(tint)
            }
        }
    }

    private var tint: Color {
        switch connections.state {
        case .refusing: .red
        case .saturated: .orange
        default: .secondary
        }
    }

    private var title: String {
        switch connections.state {
        case .refusing: "Refusing New Connections"
        case .saturated: "Close to the Connection Limit"
        default: "Connections Unchecked"
        }
    }

    private var advice: String {
        switch connections.state {
        case .refusing:
            "Applications on this pool cannot open database connections. Release connections held by its databases, or raise max_connections in the Studio."
        case .saturated:
            "New connections are refused once the limit is reached. Check which databases hold them."
        default:
            "Whether applications can connect is not known right now."
        }
    }
}

/// One instance pod: name, role, node, readiness and restarts.
private struct MemberRow: View {
    let member: DatabasePoolDetail.Member

    var body: some View {
        HStack(spacing: 12) {
            Circle()
                .fill(member.ready ? Color.green : .orange)
                .frame(width: 8, height: 8)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(member.name).font(.body.monospaced()).lineLimit(1)
                    if !member.role.isEmpty { TagChip(text: member.role) }
                }
                Text(detail)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
        }
        .accessibilityElement(children: .combine)
    }

    private var detail: String {
        var parts: [String] = [member.ready ? "ready" : member.phase.lowercased()]
        if let node = member.node { parts.append("on \(node)") }
        if member.restarts > 0 { parts.append(member.restarts == 1 ? "1 restart" : "\(member.restarts) restarts") }
        if let started = member.startedAt {
            parts.append("up \(compactAge(since: started))")
        }
        return parts.joined(separator: " · ")
    }
}

/// The charts a pool offers, one at a time on a phone.
enum PoolMetric: String, CaseIterable, Identifiable {
    case connections, transactions, cpu, memory, cache, storage

    var id: String { rawValue }

    var title: String {
        switch self {
        case .connections: "Connections"
        case .transactions: "Transactions"
        case .cpu: "CPU"
        case .memory: "Memory"
        case .cache: "Cache Hit Ratio"
        case .storage: "Storage"
        }
    }

    var yDomain: ClosedRange<Double>? {
        self == .cache ? 0...1 : nil
    }

    func series(_ metrics: DatabasePoolMetrics) -> [MetricSeries] {
        let dates = metrics.timestamps
        func line<V>(_ label: String, _ color: Color, _ values: [V?], _ value: (V) -> Double) -> MetricSeries {
            MetricSeries(label: label, color: color, points: zip(dates, values).map { ($0, $1.map(value)) })
        }
        switch self {
        case .connections: return [line("Connections", .usageCPU, metrics.connections) { Double($0) }]
        case .transactions:
            return [
                line("Commits", .usageCPU, metrics.commits) { Double($0) },
                line("Rollbacks", .red, metrics.rollbacks) { Double($0) },
            ]
        case .cpu: return [line("CPU", .usageCPU, metrics.cpuMillicores) { Double($0) }]
        case .memory: return [line("Memory", .usageMemory, metrics.memoryBytes) { Double($0) }]
        case .cache: return [line("Hit Ratio", .usageCPU, metrics.cacheHitRatio) { $0 }]
        case .storage: return [line("Databases", .usageDisk, metrics.databaseBytes) { Double($0) }]
        }
    }

    func format(_ value: Double) -> String {
        switch self {
        case .connections, .transactions: Int(value.rounded()).formatted(.number.notation(.compactName))
        case .cpu: value >= 1000 ? "\(formatCores(Int(value)))" : "\(Int(value))m"
        case .memory, .storage: formatBytes(Int64(value))
        case .cache: "\(Int((value * 100).rounded()))%"
        }
    }

    func footnote(stepSeconds: Int?) -> String {
        switch self {
        case .connections: "Client connections, from the primary."
        case .transactions:
            "Committed and rolled back transactions per \(stepSeconds.map(Self.step) ?? "bucket"), from the primary."
        case .cpu: "CPU of every instance, summed. Pools carry no CPU limit."
        case .memory: "Memory of every instance, summed."
        case .cache: "Share of block reads served from the buffer cache."
        case .storage: "Logical size of every database on the pool."
        }
    }

    private static func step(_ seconds: Int) -> String {
        if seconds >= 3600 { return seconds == 3600 ? "hour" : "\(seconds / 3600) hours" }
        if seconds >= 60 { return seconds == 60 ? "minute" : "\(seconds / 60) minutes" }
        return "\(seconds) seconds"
    }
}

#Preview {
    PreviewHost { DatabasePoolView(name: "shared") }
}
