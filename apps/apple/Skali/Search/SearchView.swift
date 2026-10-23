import SkaliKit
import SwiftUI

/// Search across the selected cluster's projects. Projects are what there
/// is to find today; services and environments join once their screens exist.
struct SearchView: View {
    @Environment(ClusterSession.self) private var session
    @State private var query = ""

    var body: some View {
        List(results) { project in
            NavigationLink(value: project) {
                ProjectRow(project: project)
            }
        }
        .overlay {
            if !query.isEmpty && results.isEmpty {
                ContentUnavailableView.search(text: query)
            }
        }
        .navigationTitle("Search")
        .navigationDestination(for: Project.self) { ProjectDetailView(project: $0) }
        .searchable(text: $query, prompt: "Projects")
        .task { if session.projects == nil { await session.loadProjects() } }
    }

    private var results: [Project] {
        let projects = ProjectSort.nameAscending.sorted(session.projects ?? [])
        guard !query.isEmpty else { return projects }
        return projects.filter { project in
            project.label.localizedStandardContains(query)
                || project.name.localizedStandardContains(query)
                || project.environmentRollup.contains { $0.name.localizedStandardContains(query) }
        }
    }
}
