import SkaliKit
import SwiftUI

/// The project list: every project the account can see, sorted the way the
/// person chose, with pull to refresh.
struct ProjectsView: View {
    @Environment(ClusterSession.self) private var session
    @AppStorage("projects.sort") private var sort: ProjectSort = .nameAscending
    @AppStorage("projects.mineOnly") private var mineOnly = false

    var body: some View {
        content
            .navigationTitle("Projects")
            .navigationSubtitle(subtitle)
            .toolbar { optionsMenu }
            .navigationDestination(for: Project.self) { ProjectDetailView(project: $0) }
            .task { await session.loadProjects() }
            .refreshable { await session.loadProjects() }
    }

    @ViewBuilder private var content: some View {
        if let projects = session.projects {
            let visible = visibleProjects(projects)
            List(visible) { project in
                NavigationLink(value: project) {
                    ProjectRow(project: project)
                }
            }
            .overlay {
                if visible.isEmpty { emptyState(hasAny: !projects.isEmpty) }
            }
        } else if let error = session.projectsError {
            ContentUnavailableView {
                Label("Projects unavailable", systemImage: "exclamationmark.triangle")
            } description: {
                Text(error)
            } actions: {
                Button("Try Again") { Task { await session.loadProjects() } }
            }
        } else {
            ProgressView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private func visibleProjects(_ projects: [Project]) -> [Project] {
        // The scope only exists for instance admins: everyone else sees
        // their memberships and nothing more.
        let scoped = session.user.isAdmin && mineOnly ? projects.filter(\.access.member) : projects
        return sort.sorted(scoped)
    }

    @ViewBuilder private func emptyState(hasAny: Bool) -> some View {
        if hasAny {
            ContentUnavailableView {
                Label("No Memberships", systemImage: "person.crop.circle.badge.questionmark")
            } description: {
                Text("You see every project as an instance admin, but none lists you as a member.")
            } actions: {
                Button("Show All Projects") { mineOnly = false }
            }
        } else {
            ContentUnavailableView {
                Label("No Projects", systemImage: "square.grid.2x2")
            } description: {
                Text(session.user.isAdmin || session.user.createProjects
                     ? "Run skali deploy in a repository to create one."
                     : "Projects appear once a project admin grants you a role on them.")
            }
        }
    }

    private var subtitle: String {
        var parts = [session.instanceLabel]
        if let projects = session.projects {
            parts.append(projects.count == 1 ? "1 project" : "\(projects.count) projects")
        }
        parts.append(sort.shortLabel)
        return parts.joined(separator: " · ")
    }

    @ToolbarContentBuilder private var optionsMenu: some ToolbarContent {
        ToolbarItem(placement: .primaryAction) {
            Menu {
                Picker("Sort By", selection: $sort) {
                    ForEach(ProjectSort.allCases) { sort in
                        Text(sort.menuLabel).tag(sort)
                    }
                }
                if session.user.isAdmin {
                    Picker("Show", selection: $mineOnly) {
                        Text("All Projects").tag(false)
                        Text("My Projects").tag(true)
                    }
                }
            } label: {
                Label("Options", systemImage: "ellipsis")
            }
        }
    }
}

extension ProjectSort {
    var menuLabel: String {
        switch self {
        case .nameAscending: "Name, A to Z"
        case .nameDescending: "Name, Z to A"
        case .updatedNewest: "Updated, Newest First"
        case .updatedOldest: "Updated, Oldest First"
        }
    }

    var shortLabel: String {
        switch self {
        case .nameAscending, .nameDescending: "by name"
        case .updatedNewest, .updatedOldest: "by activity"
        }
    }
}

#Preview {
    PreviewHost { ProjectsView() }
}
