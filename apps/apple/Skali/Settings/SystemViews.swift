import SkaliKit
import SwiftUI

// First, read-only versions of the screens behind the Settings rows.

struct NodesView: View {
    let nodes: [ClusterNode]

    var body: some View {
        List(nodes) { node in
            LabeledContent {
                Text(node.ready ? "Online" : "Offline")
                    .foregroundStyle(node.ready ? .green : .secondary)
            } label: {
                Text(node.name).monospaced()
                if let role = node.role { Text(role) }
            }
        }
        .overlay {
            if nodes.isEmpty { ContentUnavailableView("No Nodes", systemImage: "server.rack") }
        }
        .navigationTitle("Nodes")
    }
}

struct DatabasePoolsView: View {
    let pools: [DatabasePool]

    var body: some View {
        List(pools) { pool in
            LabeledContent {
                if let state = pool.state { Text(state) }
            } label: {
                Text(pool.name).monospaced()
                if let engine = pool.engine {
                    Text(pool.major.map { "\(engine) \($0)" } ?? engine)
                }
            }
        }
        .overlay {
            if pools.isEmpty { ContentUnavailableView("No Pools", systemImage: "cylinder") }
        }
        .navigationTitle("Databases")
    }
}

struct BackupTargetView: View {
    let backup: SystemOverview.Backup

    var body: some View {
        List {
            if case .set(let target) = backup {
                LabeledContent("Name", value: target.name)
                LabeledContent("Bucket", value: target.bucket)
                LabeledContent("Endpoint", value: target.endpoint)
            }
        }
        .overlay {
            switch backup {
            case .none: ContentUnavailableView("No Backup Target", systemImage: "archivebox",
                                               description: Text("Set one in the Studio to back up databases off the cluster."))
            case .unavailable: ContentUnavailableView("Unavailable", systemImage: "exclamationmark.triangle")
            case .unknown, .set: EmptyView()
            }
        }
        .navigationTitle("Backup Target")
    }
}

struct PlaceholderView: View {
    let title: String
    let systemImage: String

    var body: some View {
        ContentUnavailableView(title, systemImage: systemImage, description: Text("Coming soon."))
            .navigationTitle(title)
    }
}
