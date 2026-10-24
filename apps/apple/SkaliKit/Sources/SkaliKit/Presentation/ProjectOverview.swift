import Foundation

// What a project's overview shows for one environment, built from the
// definition, the live status, the usage series and the storage samples.
// The Studio's project page (studio/src/routes/(app)/projects/[project])
// draws the same numbers.

extension [ProjectEnvironment] {
    /// The environment to show: the one named (current or former name),
    /// else production, else the first the caller may read, else the
    /// first at all, as in the Studio.
    public func preferred(named name: String?) -> ProjectEnvironment? {
        if let name, let named = first(where: { $0.answers(to: name) }) { return named }
        let open = filter { !$0.isLocked }
        return open.first { $0.name == "production" } ?? open.first ?? first
    }
}

extension ProjectEnvironment {
    /// "active", "stopped", "deleting": the Studio's words for the states.
    public var stateLabel: String? {
        state.map(Self.stateLabel)
    }

    static func stateLabel(_ state: String) -> String {
        switch state {
        case "down": "stopped"
        case "releasing": "deleting"
        default: state
        }
    }
}

// MARK: Services

/// Where a service of the definition stands in the environment.
public enum ServiceLiveState: Hashable, Sendable {
    /// No status document yet.
    case pending
    /// The draft has it; the environment does not run it (yet).
    case notDeployed
    case health(ServiceHealth)
}

/// One service of the definition with its live state: a row of the
/// overview's Services section.
public struct ProjectServiceItem: Hashable, Sendable, Identifiable {
    public let kind: ServiceKind
    public let key: String
    /// "build", "postgres 17", "private".
    public let detail: String
    public let state: ServiceLiveState
    /// Ready pods of all pods, for applications that run some.
    public let pods: PodCount?

    public var id: String { "\(kind.rawValue):\(key)" }

    /// "build · 2/2", "postgres 17": the Studio card's line without the
    /// kind, which the row's tile shows.
    public var detailLine: String {
        var parts = [detail]
        if let pods { parts.append("\(pods.ready)/\(pods.total)") }
        return parts.joined(separator: " · ")
    }

}

public struct PodCount: Hashable, Sendable {
    public let ready: Int
    public let total: Int
}

extension ProjectDefinition {
    /// Applications, then databases, then buckets, each by key, as the
    /// Studio lists them.
    public func serviceItems(status: EnvironmentStatus?) -> [ProjectServiceItem] {
        func state(_ kind: ServiceKind, _ key: String) -> (ServiceLiveState, ServiceStatus?) {
            guard let status else { return (.pending, nil) }
            guard let live = status.service(kind, key) else { return (.notDeployed, nil) }
            return (.health(live.health), live)
        }
        var items: [ProjectServiceItem] = []
        for (key, application) in (applications ?? [:]).sorted(by: { $0.key < $1.key }) {
            let (state, live) = state(.application, key)
            let pods = live.flatMap { live in
                live.pods.isEmpty ? nil : PodCount(ready: live.pods.filter(\.ready).count, total: live.pods.count)
            }
            items.append(ProjectServiceItem(
                kind: .application, key: key, detail: application.source.kind == "image" ? "image" : "build",
                state: state, pods: pods))
        }
        for (key, database) in (databases ?? [:]).sorted(by: { $0.key < $1.key }) {
            items.append(ProjectServiceItem(
                kind: .database, key: key, detail: "\(database.engine) \(database.version)",
                state: state(.database, key).0, pods: nil))
        }
        for (key, bucket) in (buckets ?? [:]).sorted(by: { $0.key < $1.key }) {
            items.append(ProjectServiceItem(
                kind: .bucket, key: key, detail: bucket.visibility, state: state(.bucket, key).0, pods: nil))
        }
        return items
    }
}

// MARK: Environment rollup

extension EnvironmentStatus {
    /// The dot beside the summary: green only when every service is
    /// healthy, red when one is unhealthy, amber in between, unknown
    /// without services.
    public var overallHealth: ServiceHealth {
        guard !services.isEmpty else { return .unknown }
        if services.allSatisfy({ $0.health == .healthy }) { return .healthy }
        if services.contains(where: { $0.health == .unhealthy }) { return .unhealthy }
        return .degraded
    }

    public var deferredRoutes: Int {
        services.reduce(0) { $0 + $1.deferredRoutes }
    }
}

/// "3 services · 3/3 healthy · active": the line under the project's name.
public func environmentSummary(serviceCount: Int, status: EnvironmentStatus?) -> String {
    var parts = [serviceCount == 1 ? "1 service" : "\(serviceCount) services"]
    if let status {
        if !status.services.isEmpty {
            let healthy = status.services.filter { $0.health == .healthy }.count
            parts.append("\(healthy)/\(status.services.count) healthy")
        }
        parts.append(ProjectEnvironment.stateLabel(status.state))
        let deferred = status.deferredRoutes
        if deferred > 0 { parts.append(deferred == 1 ? "1 route deferred" : "\(deferred) routes deferred") }
    }
    return parts.joined(separator: " · ")
}

// MARK: Usage

/// The declared limits of an environment's applications, scaled by the
/// pods running (else the minimum replicas): the denominators of the
/// usage tiles. An approximation when the running revision trails the
/// draft, as in the Studio.
public struct UsageLimits: Hashable, Sendable {
    public var cpuMillicores: Int?
    public var memoryBytes: Int64?

    public init(cpuMillicores: Int? = nil, memoryBytes: Int64? = nil) {
        self.cpuMillicores = cpuMillicores
        self.memoryBytes = memoryBytes
    }

    public init(definition: ProjectDefinition?, status: EnvironmentStatus?) {
        var cpu = 0
        var memory: Int64 = 0
        for (key, application) in definition?.applications ?? [:] {
            guard let limits = application.resources?.limits else { continue }
            let running = status?.service(.application, key)?.pods.count ?? 0
            let replicas = running > 0 ? running : max(1, application.scaling.minReplicas)
            cpu += (limits.milliCpu ?? 0) * replicas
            memory += (limits.memoryBytes ?? 0) * Int64(replicas)
        }
        cpuMillicores = cpu > 0 ? cpu : nil
        memoryBytes = memory > 0 ? memory : nil
    }
}

/// One usage tile: the newest value over the window's sparkline.
public struct UsageStat: Hashable, Sendable, Identifiable {
    public enum Kind: String, CaseIterable, Sendable {
        case requests, cpu, memory, traffic, replicas, restarts

        public var title: String {
            switch self {
            case .requests: "Requests"
            case .cpu: "CPU"
            case .memory: "Memory"
            case .traffic: "Traffic"
            case .replicas: "Replicas"
            case .restarts: "Restarts"
            }
        }
    }

    public let kind: Kind
    /// Nil when the window holds no samples.
    public let value: String?
    public let unit: String
    public let points: [Double?]
    public let footnote: String

    public var id: Kind { kind }

    /// A tile without samples.
    public static func empty(_ kind: Kind) -> UsageStat {
        UsageStat(kind: kind, value: nil, unit: "", points: [], footnote: "no data yet")
    }
}

extension EnvironmentMetrics {
    /// The four tiles over every application, summed: requests per
    /// minute, CPU and memory against the limits, and edge traffic over
    /// the window. Port of the Studio's usageStats.
    public func usageStats(limits: UsageLimits) -> [UsageStat] {
        [requestsStat(), cpuStat(limits: limits), memoryStat(limits: limits), trafficStat()]
    }

    /// The same series for one application only.
    public func only(_ key: String) -> EnvironmentMetrics {
        EnvironmentMetrics(
            window: window, stepSeconds: stepSeconds, timestamps: timestamps,
            applications: applications.filter { $0.key == key })
    }

    /// One application's tiles. One without a public route has no edge
    /// traffic by construction, so its requests and traffic give way to
    /// its replicas and restarts, what a worker's health is about.
    public func applicationStats(key: String, spec: ApplicationSpec, status: ServiceStatus?) -> [UsageStat] {
        let mine = only(key)
        let limits = UsageLimits(
            definition: ProjectDefinition(applications: [key: spec]),
            status: status.map { EnvironmentStatus(environmentId: "", services: [$0]) })
        let stats = mine.usageStats(limits: limits)
        guard spec.routes?.isEmpty ?? true else { return stats }
        let pods = status?.pods ?? []
        let ready = pods.filter(\.ready).count
        let restarts = pods.reduce(0) { $0 + $1.restarts }
        let replicas = UsageStat(
            kind: .replicas, value: status == nil ? nil : "\(ready)", unit: "/ \(pods.count) ready", points: [],
            footnote: "scale \(spec.scaleLabel)")
        let restartStat = UsageStat(
            kind: .restarts, value: status == nil ? nil : "\(restarts)", unit: "", points: [],
            footnote: pods.isEmpty ? "no pods observed" : "across \(pods.count) \(pods.count == 1 ? "pod" : "pods")")
        return [replicas, stats[1], stats[2], restartStat]
    }

    private func requestsStat() -> UsageStat {
        let series = sum { $0.edge?.requests.map { $0.map(Double.init) } }
        guard let current = currentTotal(series) else { return .empty(.requests) }
        let perMinute = 60 / Double(max(1, stepSeconds))
        let rate = series.map { $0.map { $0 * perMinute } }
        return UsageStat(
            kind: .requests, value: formatCount(current * perMinute), unit: "/min", points: rate,
            footnote: "peak \(formatCount(rate.compactMap(\.self).max() ?? 0))/min")
    }

    private func cpuStat(limits: UsageLimits) -> UsageStat {
        let series = sum { $0.cpuMillicores.map { $0.map(Double.init) } }
        guard let current = currentTotal(series) else { return .empty(.cpu) }
        // The limit picks the unit so value, limit and peak all match.
        let inCores = limits.cpuMillicores.map { $0 >= 1000 } ?? (current >= 1000)
        func format(_ millicores: Double) -> String {
            inCores ? String(format: millicores < 100 ? "%.2f" : "%.1f", millicores / 1000) : "\(Int(millicores.rounded()))"
        }
        let unit: String = if let limit = limits.cpuMillicores {
            inCores ? "/ \(trimmed(Double(limit) / 1000)) \(limit == 1000 ? "core" : "cores")" : "/ \(limit) mCPU"
        } else {
            inCores ? "cores" : "mCPU"
        }
        return UsageStat(
            kind: .cpu, value: format(current), unit: unit, points: series,
            footnote: "peak \(format(series.compactMap(\.self).max() ?? current))")
    }

    private func memoryStat(limits: UsageLimits) -> UsageStat {
        let series = sum { $0.memoryBytes.map { $0.map(Double.init) } }
        guard let current = currentTotal(series) else { return .empty(.memory) }
        let (number, unit) = splitBytes(Int64(current))
        return UsageStat(
            kind: .memory, value: number,
            unit: limits.memoryBytes.map { "\(unit) / \(formatBytes($0))" } ?? unit, points: series,
            footnote: "peak \(formatBytes(Int64(series.compactMap(\.self).max() ?? current)))")
    }

    private func trafficStat() -> UsageStat {
        let incoming = sum { $0.edge?.requestBytes.map { $0.map(Double.init) } }
        let outgoing = sum { $0.edge?.responseBytes.map { $0.map(Double.init) } }
        let inTotal = incoming.compactMap(\.self).reduce(0, +)
        let outTotal = outgoing.compactMap(\.self).reduce(0, +)
        guard incoming.contains(where: { $0 != nil }) || outgoing.contains(where: { $0 != nil }) else {
            return .empty(.traffic)
        }
        let both = zip(incoming, outgoing).map { a, b in a == nil && b == nil ? nil : (a ?? 0) + (b ?? 0) }
        let (number, unit) = splitBytes(Int64(inTotal + outTotal))
        return UsageStat(
            kind: .traffic, value: number, unit: "\(unit) / \(window.rawValue)", points: both,
            footnote: "in \(formatBytes(Int64(inTotal))) · out \(formatBytes(Int64(outTotal)))")
    }

    /// The per-bucket sum over the applications; nil where none has data.
    private func sum(_ pick: (ApplicationSeries) -> [Double?]?) -> [Double?] {
        var out = [Double?](repeating: nil, count: timestamps.count)
        for application in applications {
            for (index, value) in (pick(application) ?? []).enumerated() where index < out.count {
                if let value { out[index] = (out[index] ?? 0) + value }
            }
        }
        return out
    }

    /// The newest bucket any application has data in: current usage.
    private func currentTotal(_ series: [Double?]) -> Double? {
        series.lastValue()
    }
}

/// "0.4", "12", "950", "1.2k", "34k", "2.1M": the Studio's formatCount,
/// with a decimal below ten so a quiet service does not read as zero.
public func formatCount(_ value: Double) -> String {
    switch value {
    case ..<10:
        let tenths = (value * 10).rounded() / 10
        return tenths == tenths.rounded() ? "\(Int(tenths))" : String(format: "%.1f", tenths)
    case ..<1000: return "\(Int(value.rounded()))"
    case ..<1_000_000:
        let thousands = value / 1000
        return thousands >= 10 ? "\(Int(thousands.rounded()))k" : String(format: "%.1fk", thousands)
    default:
        let millions = value / 1_000_000
        return millions >= 10 ? "\(Int(millions.rounded()))M" : String(format: "%.1fM", millions)
    }
}

/// "8.4 MiB" as ("8.4", "MiB").
func splitBytes(_ bytes: Int64) -> (String, String) {
    let parts = formatBytes(bytes).split(separator: " ", maxSplits: 1)
    return (String(parts[0]), parts.count > 1 ? String(parts[1]) : "")
}

/// "2", "1.5": a core count without a trailing ".0".
private func trimmed(_ value: Double) -> String {
    value == value.rounded() ? "\(Int(value))" : String(format: "%.1f", value)
}

// MARK: Storage

/// The storage of one environment: total, declared capacity, the split by
/// kind, and every service largest first.
public struct StorageRollup: Hashable, Sendable {
    public let services: [ServiceStorage]
    public let total: Int64
    /// What the services declared; 0 when none did.
    public let capacity: Int64

    public init(storage: [ServiceStorage], environmentID: String) {
        services = storage.filter { $0.environmentId == environmentID }
            .sorted { $0.footprint != $1.footprint ? $0.footprint > $1.footprint : $0.serviceKey < $1.serviceKey }
        total = services.reduce(0) { $0 + $1.footprint }
        capacity = services.reduce(0) { $0 + $1.capacityBytes }
    }

    /// Bytes per kind, in a fixed order, kinds without bytes left out.
    public var byKind: [(kind: ServiceStorage.Kind, bytes: Int64)] {
        ServiceStorage.Kind.allCases.compactMap { kind in
            let bytes = services.filter { $0.kind == kind }.reduce(0) { $0 + $1.footprint }
            return bytes > 0 ? (kind, bytes) : nil
        }
    }
}

// MARK: Activity

extension [Run] {
    /// The newest deployment, rollback or restart (runs come newest first).
    public var latestRollout: Run? { first(where: \.isRollout) }
}

extension BackupSchedule {
    /// "daily", "every 6 hours", "weekly": the schedule in a word or two;
    /// "scheduled" for shapes without one.
    public var cadence: String {
        let fields = schedule.split(separator: " ", omittingEmptySubsequences: true).map(String.init)
        guard fields.count == 5, fields[3] == "*" else { return "scheduled" }
        let (minute, hour, day, weekday) = (fields[0], fields[1], fields[2], fields[4])
        let isNumber = { (field: String) in !field.isEmpty && field.allSatisfy(\.isNumber) }
        let step = { (field: String) in field.hasPrefix("*/") ? Int(field.dropFirst(2)) : nil }
        if day == "*" && weekday == "*" {
            if let hours = step(hour), isNumber(minute) { return hours == 1 ? "hourly" : "every \(hours) hours" }
            if isNumber(minute) && hour == "*" { return "hourly" }
            if isNumber(minute) && isNumber(hour) { return "daily" }
            if let minutes = step(minute), hour == "*" { return minutes == 1 ? "every minute" : "every \(minutes) minutes" }
            return "scheduled"
        }
        if day == "*" && isNumber(weekday) && isNumber(minute) && isNumber(hour) { return "weekly" }
        if weekday == "*" && isNumber(day) && isNumber(minute) && isNumber(hour) { return "monthly" }
        return "scheduled"
    }
}
