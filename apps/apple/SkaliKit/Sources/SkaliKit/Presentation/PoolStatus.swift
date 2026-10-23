import Foundation

/// A pool's health in one word, worst first: whether applications can
/// connect, then whether its instances are ready (as the Studio's pool
/// pills read, studio/src/lib/types/pools.ts).
public enum PoolStatus: Hashable, Sendable {
    /// Logins are refused for lack of connection slots.
    case refusing
    /// 90% or more of the connections applications may open are in use.
    case saturated
    /// The operator reports something other than a healthy, fully ready cluster.
    case degraded(phase: String)
    /// Nothing observed the pool yet.
    case notObserved
    /// The connection check could not run lately.
    case connectionsUnknown
    case healthy
}

extension DatabasePool {
    public var status: PoolStatus {
        switch connections?.state {
        case .refusing: return .refusing
        case .saturated: return .saturated
        default: break
        }
        guard let observed else { return .notObserved }
        if observed.phase != "Cluster in healthy state" || observed.readyInstances != observed.instances {
            return .degraded(phase: observed.phase)
        }
        if connections?.state == .unknown { return .connectionsUnknown }
        return .healthy
    }

    /// Connections in use against what applications may open, 0...1.
    public var connectionFraction: Double? {
        guard let used = connections?.used, let limit = connections?.limit, limit > 0 else { return nil }
        return min(1, Double(used) / Double(limit))
    }

    /// "2 GiB · automatic from skali-app-05", "4 GiB · custom".
    public var budgetText: String {
        guard let bytes = memory.bytes else { return "unknown · no database node observed yet" }
        if !memory.auto { return "\(formatBytes(bytes)) · custom" }
        let from = memory.node.map { " from \($0)" } ?? ""
        return "\(formatBytes(bytes)) · automatic\(from)\(memory.capped == true ? ", capped" : "")"
    }

    /// "postgres 18".
    public var engineLabel: String { "\(engine) \(major)" }
}
