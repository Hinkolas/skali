import SkaliKit
import SwiftUI

/// Every database on a pool, as a sheet: searchable, largest first, the
/// projects' databases apart from the platform's own.
struct PoolDatabasesView: View {
    let detail: DatabasePoolDetail

    @Environment(\.dismiss) private var dismiss
    @State private var query = ""

    var body: some View {
        let databases = filtered
        let projects = databases.filter { !$0.isSystem }
        let system = databases.filter(\.isSystem)
        List {
            if !projects.isEmpty {
                Section("Projects · \(projects.count)") {
                    ForEach(projects) { PoolDatabaseRow(database: $0) }
                }
            }
            if !system.isEmpty {
                Section {
                    ForEach(system) { PoolDatabaseRow(database: $0) }
                } header: {
                    Text("System · \(system.count)")
                } footer: {
                    Text("The platform's own databases.")
                }
            }
        }
        .overlay {
            if databases.isEmpty && !query.isEmpty {
                ContentUnavailableView.search(text: query)
            }
        }
        .searchable(text: $query, placement: .navigationBarDrawer(displayMode: .always), prompt: "Databases or projects")
        .navigationTitle("Databases")
        .navigationSubtitle(detail.pool.name)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button("Done") { dismiss() }
            }
        }
    }

    private var filtered: [DatabasePoolDetail.Database] {
        let databases = detail.databasesBySize
        guard !query.isEmpty else { return databases }
        return databases.filter {
            $0.databaseName.localizedStandardContains(query) || $0.ownerLabel.localizedStandardContains(query)
        }
    }
}
