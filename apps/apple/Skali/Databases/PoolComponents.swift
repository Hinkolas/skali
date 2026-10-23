import SkaliKit
import SwiftUI

extension PoolStatus {
    var label: String {
        switch self {
        case .refusing: "Refusing"
        case .saturated: "Near Limit"
        case .degraded: "Degraded"
        case .notObserved: "Not Observed"
        case .connectionsUnknown: "Unchecked"
        case .healthy: "Healthy"
        }
    }

    var tint: Color {
        switch self {
        case .refusing: .red
        case .saturated, .degraded: .orange
        case .notObserved, .connectionsUnknown: .secondary
        case .healthy: .green
        }
    }

    /// The pool's tile: the database color while healthy, the state's otherwise.
    var tileTint: Color {
        self == .healthy ? .cyan : tint
    }

    /// What the list's second line says instead of the facts, when the
    /// pool needs a look.
    var rowNote: String? {
        switch self {
        case .refusing: "refusing connections"
        case .saturated: "near connection limit"
        case .degraded(let phase): phase.lowercased()
        case .notObserved: "not observed"
        case .connectionsUnknown, .healthy: nil
        }
    }
}

extension DatabasePool {
    /// The connection bar's color: amber from 90% on, where the daemon
    /// calls the pool saturated.
    var connectionColor: Color {
        (connectionFraction ?? 0) >= 0.9 ? .orange : .usageCPU
    }

    /// "shared", with what the class means.
    var classDescription: String {
        switch self.class {
        case "shared": "Shared · every project's databases"
        case "environment": "Environment · one pool per environment"
        case "dedicated": "Dedicated · one database"
        default: self.class
        }
    }
}

/// A database on a pool: its name, whose it is, and its size.
struct PoolDatabaseRow: View {
    let database: DatabasePoolDetail.Database

    @ScaledMetric(relativeTo: .body) private var tile: CGFloat = 30

    var body: some View {
        HStack(spacing: 12) {
            ResourceTile(
                systemImage: database.isSystem ? "gearshape" : "cylinder.split.1x2",
                tint: database.isSystem ? .secondary : .cyan, size: tile)
            VStack(alignment: .leading, spacing: 2) {
                // Database names are long identifiers: wrap rather than hide a
                // part, and break after an underscore, never with a hyphen the
                // name does not have.
                Text(database.databaseName.wrappingAtSeams)
                    .font(.subheadline.monospaced())
                    .lineLimit(2)
                    .accessibilityLabel(database.databaseName)
                Text(database.ownerLabel)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer(minLength: 8)
            VStack(alignment: .trailing, spacing: 2) {
                Text(database.usedBytes.map(formatBytes) ?? "—")
                    .font(.subheadline.monospacedDigit())
                if let connections = database.connections {
                    Text(connections == 1 ? "1 connection" : "\(connections) connections")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
        }
        .accessibilityElement(children: .combine)
    }
}
