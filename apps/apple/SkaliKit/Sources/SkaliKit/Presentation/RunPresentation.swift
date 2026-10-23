import Foundation

// How runs read in lists and sheets, matching the Studio
// (studio/src/lib/types/runs.ts, models/rundeployment.ts, format.ts).

/// Which runs a list shows.
public enum RunFilter: String, CaseIterable, Identifiable, Sendable {
    /// Deploys, rollbacks, restarts, stops and deletes: what changes what
    /// the environment runs.
    case rollouts
    /// Snapshots, restores and accepted restores.
    case backups
    case all

    public var id: String { rawValue }

    public func includes(_ run: Run) -> Bool {
        switch self {
        case .rollouts: ["deployment", "rollback", "restart", "teardown", "purge"].contains(run.kind)
        case .backups: ["backup", "restore", "restore-accept"].contains(run.kind)
        case .all: true
        }
    }
}

extension Run {
    /// "deployment", "stop", "delete": the API's kind in the Studio's words.
    public var kindLabel: String {
        switch kind {
        case "teardown": "stop"
        case "purge": "delete"
        case "restore-accept": "restore accept"
        default: kind
        }
    }

    /// "01a101fc": the first block of the id, as the CLI shows it.
    public var shortID: String { String(id.prefix(8)) }

    /// How long the run took, or has taken so far; nil before it started.
    public func duration(now: Date = .now) -> String? {
        startedAt.map { formatDuration(from: $0, to: finishedAt ?? now) }
    }

    /// Whether cancelling undoes anything: a stop or a delete is a decision
    /// the reconciler carries out whatever happens to its run.
    public var isCancellable: Bool { isActive && kind != "teardown" && kind != "purge" }

    /// "from staging · web, worker updated · main created · 2 values
    /// changed": what a rollout changed; nil for runs that record nothing.
    public var changeSummary: String? {
        guard let deployment else { return nil }
        var parts: [String] = []
        if let source = deployment.fromEnvironment { parts.append("from \(source)") }
        if kind == "rollback", let revision = deployment.revision {
            parts.append("to \(revision.replacingOccurrences(of: "sha256:", with: "").prefix(8))")
        }
        let changes = deployment.changes ?? []
        var actions = ["update", "create", "remove"]
        for change in changes where !actions.contains(change.action) { actions.append(change.action) }
        for action in actions {
            let services = changes.filter { $0.action == action }.map(\.service)
            if !services.isEmpty { parts.append("\(serviceNames(services)) \(actionWord(action))") }
        }
        let values = Set((deployment.values ?? []).map(\.name)).count
        if values > 0 { parts.append(values == 1 ? "1 value changed" : "\(values) values changed") }
        let restarts = deployment.restarts ?? []
        if !restarts.isEmpty { parts.append("\(serviceNames(restarts)) restarted") }
        if changes.isEmpty && values == 0 && restarts.isEmpty { parts.append("no changes") }
        return parts.joined(separator: " · ")
    }
}

/// "created", "updated": a plan action in the past tense.
public func actionWord(_ action: String) -> String {
    switch action {
    case "create": "created"
    case "update": "updated"
    case "remove": "removed"
    case "restart": "restarted"
    case "prune": "pruned"
    default: action
    }
}

/// "applications.web" as its bare key, "web".
public func serviceName(_ dotted: String) -> String {
    for prefix in ["applications.", "databases.", "buckets."] where dotted.hasPrefix(prefix) {
        return String(dotted.dropFirst(prefix.count))
    }
    return dotted
}

/// Names past three collapse into a count, so a summary stays one line.
private func serviceNames(_ services: [String]) -> String {
    let names = services.map(serviceName)
    guard names.count > 3 else { return names.joined(separator: ", ") }
    return "\(names.prefix(2).joined(separator: ", ")) and \(names.count - 2) more"
}

/// "42s", "5m 5s", "1h 12m".
public func formatDuration(from start: Date, to end: Date) -> String {
    let seconds = max(0, Int(end.timeIntervalSince(start).rounded()))
    if seconds < 60 { return "\(seconds)s" }
    let minutes = seconds / 60
    if minutes < 60 { return "\(minutes)m \(seconds % 60)s" }
    return "\(minutes / 60)h \(minutes % 60)m"
}

/// Who started a run: a person by name, "schedule", "system", or the id's
/// first block when the directory does not know it.
public func describeActor(_ actor: String, directory: [String: DirectoryUser]) -> String {
    if actor == "schedule" || actor.hasPrefix("schedule:") { return "schedule" }
    if actor.hasPrefix("system:") { return "system" }
    if let user = directory[actor] { return user.name.isEmpty ? user.email : user.name }
    if UUID(uuidString: actor) != nil { return String(actor.prefix(8)) }
    return actor
}

extension [Run] {
    /// The runs by the day they were created, newest day first; runs keep
    /// their order within a day.
    public func groupedByDay(calendar: Calendar = .current) -> [(day: Date, runs: [Run])] {
        var groups: [(day: Date, runs: [Run])] = []
        for run in self {
            let day = calendar.startOfDay(for: run.createdAt)
            if let last = groups.indices.last, groups[last].day == day {
                groups[last].runs.append(run)
            } else {
                groups.append((day, [run]))
            }
        }
        return groups
    }

    /// The run the environment works on now: the running one, else the
    /// newest still queued.
    public var activeRun: Run? {
        first { $0.status == "running" } ?? first { $0.status == "pending" }
    }

    /// The newest deployment that went through.
    public var lastSucceededDeployment: Run? {
        first { $0.kind == "deployment" && $0.status == "succeeded" }
    }
}

extension RunTree {
    /// The share of top-level steps that reached an end; 0 before the
    /// reconciler has recorded any.
    public var progress: Double {
        guard !steps.isEmpty else { return 0 }
        let settled = steps.filter { !["pending", "waiting", "running"].contains($0.status) }.count
        return Double(settled) / Double(steps.count)
    }

    /// Routes whose TLS the run set aside: a success with warnings.
    public var deferredRoutes: Int {
        func count(_ steps: [RunStep]) -> Int {
            steps.reduce(0) { $0 + ($1.isDeferredTLS ? 1 : 0) + count($1.children ?? []) }
        }
        return count(steps)
    }
}

// MARK: Progress per service

/// A run's work for one service, the way the CLI lists a deploy: the
/// steps that do it, where it stands, and how long it took. Port of the
/// Studio's serviceProgress (studio/src/lib/models/runservices.ts).
public struct ServiceProgress: Hashable, Sendable, Identifiable {
    /// The dotted name; nil for the environment's own work (validate,
    /// render, prune, verify, activate).
    public let service: String?
    /// Nil for the environment and for a collection this app does not know.
    public let kind: ServiceKind?
    /// The bare key ("web"), or "environment".
    public let name: String
    /// The steps that do the work, in journal order; parents only group.
    public let steps: [RunStep]
    public let status: String
    /// The step the row names: the one in flight, else the one waiting or
    /// queued, else the one that failed, else the last.
    public let current: RunStep
    public let startedAt: Date?
    /// Nil while any of the work is unsettled.
    public let finishedAt: Date?

    public var id: String { service ?? "" }

    public func duration(now: Date = .now) -> String? {
        startedAt.map { formatDuration(from: $0, to: finishedAt ?? now) }
    }
}

extension RunTree {
    /// The steps grouped by the service each records: applications,
    /// databases, buckets (each by key), the environment last. Nil when no
    /// step names a service (a backup, a run from an older daemon): the
    /// step tree is then the only view.
    public var serviceProgress: [ServiceProgress]? {
        var order: [String?] = []
        var groups: [String?: (leaves: [RunStep], parents: [RunStep])] = [:]
        var tagged = false
        func walk(_ steps: [RunStep], inherited: String?) {
            for step in steps {
                let service = step.service ?? inherited
                if service != nil { tagged = true }
                if groups[service] == nil {
                    groups[service] = ([], [])
                    order.append(service)
                }
                if let children = step.children, !children.isEmpty {
                    // A parent that names its service (an application's
                    // artifacts) still counts toward its status.
                    if step.service != nil { groups[service]?.parents.append(step) }
                    walk(children, inherited: service)
                } else {
                    groups[service]?.leaves.append(step)
                }
            }
        }
        walk(steps, inherited: nil)
        guard tagged else { return nil }

        let kindOrder: [ServiceKind?] = [.application, .database, .bucket, nil]
        return order.compactMap { service -> ServiceProgress? in
            guard let group = groups[service], !group.leaves.isEmpty else { return nil }
            return Self.summarize(service, steps: group.leaves, counted: group.leaves + group.parents)
        }
        .sorted { a, b in
            if (a.service == nil) != (b.service == nil) { return b.service == nil }
            let ka = kindOrder.firstIndex(of: a.kind) ?? 3, kb = kindOrder.firstIndex(of: b.kind) ?? 3
            return ka != kb ? ka < kb : a.name < b.name
        }
    }

    private static func summarize(_ service: String?, steps: [RunStep], counted: [RunStep]) -> ServiceProgress {
        let statusOrder = ["failed", "running", "waiting", "pending", "cancelled", "succeeded", "skipped"]
        let status = statusOrder.first { status in counted.contains { $0.status == status } } ?? "pending"
        let current = steps.first { $0.status == "running" } ?? steps.first { $0.status == "waiting" }
            ?? steps.first { $0.status == "pending" } ?? steps.first { $0.status == "failed" } ?? steps[steps.count - 1]
        let started = counted.filter { $0.status != "pending" }.compactMap(\.createdAt).min()
        let unsettled = counted.contains { ["pending", "waiting", "running"].contains($0.status) }
        let finished = unsettled ? nil : counted.compactMap(\.finishedAt).max()
        let (kind, name) = service.map(Self.split) ?? (nil, "environment")
        return ServiceProgress(
            service: service, kind: kind, name: name, steps: steps, status: status, current: current,
            startedAt: started, finishedAt: finished)
    }

    /// "applications.web" is the application "web"; an unknown collection
    /// keeps its dotted name.
    private static func split(_ service: String) -> (ServiceKind?, String) {
        guard let dot = service.firstIndex(of: ".") else { return (nil, service) }
        let kind: ServiceKind? = switch service[..<dot] {
        case "applications": .application
        case "databases": .database
        case "buckets": .bucket
        default: nil
        }
        return kind == nil ? (nil, service) : (kind, String(service[service.index(after: dot)...]))
    }

    /// The step the run is on: the deepest one in flight, else waiting.
    public var currentStep: RunStep? {
        func find(_ steps: [RunStep], _ status: String) -> RunStep? {
            for step in steps {
                if let inner = find(step.children ?? [], status) { return inner }
                if step.status == status { return step }
            }
            return nil
        }
        return find(steps, "running") ?? find(steps, "waiting")
    }

    /// Leaf steps that reached an end, of all leaf steps: "5 of 12 steps".
    public var leafCounts: (done: Int, total: Int) {
        func leaves(_ steps: [RunStep]) -> [RunStep] {
            steps.flatMap { ($0.children ?? []).isEmpty ? [$0] : leaves($0.children ?? []) }
        }
        let all = leaves(steps)
        return (all.filter { !["pending", "waiting", "running"].contains($0.status) }.count, all.count)
    }
}

// MARK: One service's history

/// What one run did to one service.
public struct ServiceChange: Hashable, Sendable {
    public let run: Run
    /// `create`, `update`, `remove` or `restart`.
    public let action: String
    public let detail: String?
    public let destructive: Bool
}

extension Run {
    /// What this run did to the service with the dotted name: its plan
    /// change, new versions of values it uses (which roll its pods though
    /// its own plan row may say nothing), a restart, or several; nil when
    /// the run left it alone. Port of the Studio's serviceChange.
    public func change(to service: String) -> ServiceChange? {
        guard let deployment else { return nil }
        let change = deployment.changes?.first { $0.service == service }
        let restarted = deployment.restarts?.contains(service) ?? false
        var values: [String] = []
        if change == nil || change?.action == "update" {
            for value in deployment.values ?? [] where value.services?.contains(service) == true && !values.contains(value.name) {
                values.append(value.name)
            }
        }
        let valuesNote = values.isEmpty ? nil : "\(values.count == 1 ? "value" : "values") \(values.joined(separator: ", ")) changed"
        guard let action = change?.action ?? (valuesNote != nil ? "update" : restarted ? "restart" : nil) else {
            return nil
        }
        let detail = [change?.detail, valuesNote, restarted && action != "restart" ? "restarted" : nil]
            .compactMap(\.self).joined(separator: "; ")
        return ServiceChange(
            run: self, action: action, detail: detail.isEmpty ? nil : detail, destructive: change?.destructive == true)
    }
}

extension [Run] {
    /// The runs that changed one service, newest first.
    public func changes(to service: String) -> [ServiceChange] {
        compactMap { $0.change(to: service) }
    }

    /// The newest backup that went through.
    public var lastSucceededBackup: Run? {
        first { $0.kind == "backup" && $0.status == "succeeded" }
    }
}
