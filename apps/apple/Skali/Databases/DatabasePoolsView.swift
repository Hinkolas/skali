import SkaliKit
import SwiftUI

/// Every managed database pool, grouped by class, with how full its
/// connections and its volume are. Refreshes every 30 seconds on screen.
struct DatabasePoolsView: View {
    @Environment(ClusterSession.self) private var session

    var body: some View {
        let pools = session.system.pools ?? []
        List {
            ForEach(["shared", "environment", "dedicated"], id: \.self) { poolClass in
                section(poolClass, pools.filter { $0.class == poolClass })
            }
            section("other", pools.filter { !["shared", "environment", "dedicated"].contains($0.class) })
        }
        .overlay {
            if session.system.pools == nil {
                ProgressView()
            } else if pools.isEmpty {
                ContentUnavailableView(
                    "No Database Pools", systemImage: "cylinder",
                    description: Text("The first declared database creates the shared pool."))
            }
        }
        .navigationTitle("Databases")
        .largeSubtitle(subtitle(pools), dot: dot(pools))
        .task {
            while !Task.isCancelled {
                await session.loadPools()
                try? await Task.sleep(for: .seconds(30))
            }
        }
        .refreshable { await session.loadPools() }
    }

    @ViewBuilder private func section(_ poolClass: String, _ pools: [DatabasePool]) -> some View {
        if !pools.isEmpty {
            Section("\(poolClass.capitalized) · \(pools.count)") {
                ForEach(pools.sorted { $0.name < $1.name }) { pool in
                    NavigationLink(value: SettingsRoute.pool(pool.name)) {
                        PoolRow(pool: pool, usage: session.poolUsage[pool.name])
                    }
                }
            }
        }
    }

    /// "2 pools · all healthy", "2 pools · 1 needs attention".
    private func subtitle(_ pools: [DatabasePool]) -> String {
        guard !pools.isEmpty else { return "" }
        let count = pools.count == 1 ? "1 pool" : "\(pools.count) pools"
        let attention = pools.count { $0.status != .healthy }
        return "\(count) · \(attention == 0 ? "all healthy" : "\(attention) \(attention == 1 ? "needs" : "need") attention")"
    }

    private func dot(_ pools: [DatabasePool]) -> Color? {
        guard !pools.isEmpty else { return nil }
        if pools.contains(where: { $0.status == .refusing }) { return .red }
        return pools.allSatisfy { $0.status == .healthy } ? .green : .orange
    }
}

/// One pool: tile, name, engine and size, and connection and disk bars.
private struct PoolRow: View {
    let pool: DatabasePool
    let usage: DatabasePoolMetrics.Current?

    @ScaledMetric(relativeTo: .headline) private var tile: CGFloat = 38

    var body: some View {
        let status = pool.status
        HStack(spacing: 12) {
            ResourceTile(systemImage: "cylinder.split.1x2", tint: status.tileTint, size: tile)
            VStack(alignment: .leading, spacing: 3) {
                // Pool names carry the project and environment; let them wrap.
                Text(pool.name)
                    .font(.headline.monospaced())
                    .lineLimit(2)
                Text(status.rowNote ?? "\(pool.engineLabel) · \(formatBytes(pool.storageBytes))")
                    .font(.subheadline)
                    .foregroundStyle(status.rowNote == nil ? Color.secondary : status.tint)
                    .lineLimit(1)
            }
            Spacer(minLength: 8)
            RowUsageBars(bars: [
                .init(label: "conn", fraction: pool.connectionFraction, color: pool.connectionColor),
                .init(label: "disk", fraction: diskFraction, color: .usageDisk),
            ])
        }
        .padding(.vertical, 2)
        .accessibilityElement(children: .combine)
        .accessibilityValue(status.label)
    }

    /// The databases' logical size against the volume: a full volume stops the pool.
    private var diskFraction: Double? {
        guard let bytes = usage?.databaseBytes, pool.storageBytes > 0 else { return nil }
        return min(1, Double(bytes) / Double(pool.storageBytes))
    }
}

#Preview {
    PreviewHost { DatabasePoolsView() }
}
