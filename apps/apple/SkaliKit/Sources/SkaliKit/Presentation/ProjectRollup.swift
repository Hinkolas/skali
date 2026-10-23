import Foundation

// Rollups over a project summary, matching the Studio's listings
// (studio/src/lib/models/project.ts) so the app and the browser never
// disagree about a project.

extension ServiceHealth {
    /// Worse health outranks better; unknown outranks healthy so a
    /// half-observed project never reads as fine. The daemon ranks the same
    /// way (internal/module/health.go, WorstHealth).
    public var rank: Int {
        switch self {
        case .healthy: 1
        case .unknown: 2
        case .progressing: 3
        case .degraded: 4
        case .unhealthy: 5
        }
    }
}

extension Project {
    /// The worst environment health. Locked environments carry none and stay
    /// out of the rollup; a project without environments is unknown.
    public var worstHealth: ServiceHealth {
        let environments = summary?.environments ?? []
        guard !environments.isEmpty else { return .unknown }
        return environments.compactMap(\.health).reduce(.healthy) { $1.rank > $0.rank ? $1 : $0 }
    }

    /// The environments for a one-line rollup: high priority first, then the
    /// daemon's name order.
    public var environmentRollup: [SummaryEnvironment] {
        let environments = summary?.environments ?? []
        return environments.enumerated()
            .sorted { a, b in
                if a.element.isHighPriority != b.element.isHighPriority { return a.element.isHighPriority }
                return a.offset < b.offset
            }
            .map(\.element)
    }

    /// The last activity: the summary's, else the settings time.
    public var lastActivity: Date { summary?.updatedAt ?? updatedAt }

    public var serviceCount: Int { summary?.serviceCounts.total ?? 0 }
}

/// The orders a project list offers, as in the Studio's sort menu.
public enum ProjectSort: String, CaseIterable, Identifiable, Sendable {
    case nameAscending = "name-asc"
    case nameDescending = "name-desc"
    case updatedNewest = "updated-desc"
    case updatedOldest = "updated-asc"

    public var id: String { rawValue }

    /// Ties fall back to the name so the order stays put.
    public func sorted(_ projects: [Project]) -> [Project] {
        func byName(_ a: Project, _ b: Project) -> Bool {
            switch a.label.localizedStandardCompare(b.label) {
            case .orderedAscending: true
            case .orderedDescending: false
            case .orderedSame: a.name < b.name
            }
        }
        switch self {
        case .nameAscending:
            return projects.sorted(by: byName)
        case .nameDescending:
            return projects.sorted { byName($1, $0) }
        case .updatedNewest:
            return projects.sorted { a, b in
                a.lastActivity != b.lastActivity ? a.lastActivity > b.lastActivity : byName(a, b)
            }
        case .updatedOldest:
            return projects.sorted { a, b in
                a.lastActivity != b.lastActivity ? a.lastActivity < b.lastActivity : byName(a, b)
            }
        }
    }
}
