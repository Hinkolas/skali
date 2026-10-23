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
