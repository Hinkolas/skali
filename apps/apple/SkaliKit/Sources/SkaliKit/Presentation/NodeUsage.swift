import Foundation

/// A node's current load: the newest sampled CPU and memory bucket against
/// what the node can allocate, and its disk. Any part can be missing
/// before the sampler has written one.
public struct NodeUsage: Hashable, Sendable {
    public var cpuMillicores: Int?
    public var cpuAllocatableMillicores: Int?
    public var memoryBytes: Int64?
    public var memoryAllocatableBytes: Int64?
    public var diskUsedBytes: Int64?
    public var diskCapacityBytes: Int64?

    public init(
        cpuMillicores: Int? = nil, cpuAllocatableMillicores: Int? = nil, memoryBytes: Int64? = nil,
        memoryAllocatableBytes: Int64? = nil, diskUsedBytes: Int64? = nil, diskCapacityBytes: Int64? = nil
    ) {
        self.cpuMillicores = cpuMillicores
        self.cpuAllocatableMillicores = cpuAllocatableMillicores
        self.memoryBytes = memoryBytes
        self.memoryAllocatableBytes = memoryAllocatableBytes
        self.diskUsedBytes = diskUsedBytes
        self.diskCapacityBytes = diskCapacityBytes
    }

    /// Usage keyed by node name, from the metrics and the storage samples.
    public static func byNode(metrics: NodeMetrics?, storage: [NodeStorage]?) -> [String: NodeUsage] {
        var usage: [String: NodeUsage] = [:]
        for series in metrics?.nodes ?? [] {
            usage[series.name, default: NodeUsage()].cpuMillicores = series.cpuMillicores.lastValue()
            usage[series.name, default: NodeUsage()].cpuAllocatableMillicores = series.cpuAllocatableMillicores
            usage[series.name, default: NodeUsage()].memoryBytes = series.memoryBytes.lastValue()
            usage[series.name, default: NodeUsage()].memoryAllocatableBytes = series.memoryAllocatableBytes
        }
        for sample in storage ?? [] {
            usage[sample.name, default: NodeUsage()].diskUsedBytes = sample.usedBytes
            usage[sample.name, default: NodeUsage()].diskCapacityBytes = sample.capacityBytes
        }
        return usage
    }

    /// 0...1, or nil when either side is unknown.
    public var cpuFraction: Double? { Self.fraction(cpuMillicores.map(Int64.init), cpuAllocatableMillicores.map(Int64.init)) }
    public var memoryFraction: Double? { Self.fraction(memoryBytes, memoryAllocatableBytes) }
    public var diskFraction: Double? { Self.fraction(diskUsedBytes, diskCapacityBytes) }

    private static func fraction(_ used: Int64?, _ total: Int64?) -> Double? {
        guard let used, let total, total > 0 else { return nil }
        return min(1, max(0, Double(used) / Double(total)))
    }
}

extension Array {
    /// The newest non-nil value of a series: current usage.
    public func lastValue<Value>() -> Value? where Element == Value? {
        last { $0 != nil } ?? nil
    }
}

/// "512 MiB", "7.6 GiB", "80 GiB": binary units, as the Studio's formatBytes.
public func formatBytes(_ bytes: Int64) -> String {
    let units = ["B", "KiB", "MiB", "GiB", "TiB"]
    var value = Double(bytes)
    var unit = 0
    while value >= 1024 && unit < units.count - 1 {
        value /= 1024
        unit += 1
    }
    // Unlike the Studio, a whole number drops its ".0": "8 GiB".
    let number = value >= 10 || unit == 0
        ? String(Int(value.rounded()))
        : String(format: "%.1f", value).replacingOccurrences(of: ".0", with: "")
    return "\(number) \(units[unit])"
}

/// "250m", "1.5", "12": millicores below a core, cores above, as the
/// Studio's formatCores.
public func formatCores(_ millicores: Int) -> String {
    if millicores < 1000 { return "\(millicores)m" }
    let cores = Double(millicores) / 1000
    return cores >= 10 ? String(Int(cores.rounded())) : String(format: "%.1f", cores).replacingOccurrences(of: ".0", with: "")
}
