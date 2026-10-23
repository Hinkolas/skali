import SkaliKit
import SwiftUI

/// The project list: every project the account can see, sorted the way the
/// person chose, with pull to refresh.
struct ProjectsView: View {
    @Environment(AppModel.self) private var model
    @Environment(ClusterSession.self) private var session
    @AppStorage("projects.sort") private var sort: ProjectSort = .nameAscending
    @AppStorage("projects.mineOnly") private var mineOnly = false

    var body: some View {
        content
            .navigationTitle("Projects")
            .largeSubtitle(subtitle)
            .toolbar { optionsMenu }
            .navigationDestination(for: Project.self) { ProjectDetailView(project: $0) }
            .task {
                await session.loadProjects()
                openInitialProject()
            }
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

    /// A short line under the title, as Notes has ("641 notes"): the count,
    /// and which cluster once there is more than one to tell apart.
    private var subtitle: String {
        guard let projects = session.projects else { return "" }
        let count = visibleProjects(projects).count
        let projectCount = count == 1 ? "1 project" : "\(count) projects"
        guard model.accounts.count > 1 else { return projectCount }
        return "\(session.account.displayName) · \(projectCount)"
    }

    /// The project a debug launch asked for, opened once per launch.
    private func openInitialProject() {
        guard let name = AppTab.initialProject, !Self.openedInitialProject, model.projectsPath.isEmpty,
              let project = session.projects?.first(where: { $0.name == name })
        else { return }
        Self.openedInitialProject = true
        model.projectsPath.append(project)
        if let route = AppTab.initialProjectRoute { model.projectsPath.append(route) }
    }

    private static var openedInitialProject = false

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

    @ToolbarContentBuilder private var optionsMenu: some ToolbarContent {
        ToolbarItem(placement: .primaryAction) {
            // As the Music app's playlist menu: sorting is a submenu that
            // names the current order, and the scope a single toggle.
            Menu {
                Section {
                    Picker(selection: $sort) {
                        ForEach(ProjectSort.allCases) { sort in
                            Text(sort.menuLabel).tag(sort)
                        }
                    } label: {
                        Label("Sort By", systemImage: "arrow.up.arrow.down")
                    }
                    .pickerStyle(.menu)
                }
                // Only instance admins see projects they are not members of.
                if session.user.isAdmin {
                    Section {
                        Toggle(isOn: Binding { !mineOnly } set: { mineOnly = !$0 }) {
                            Label("Show All Projects", systemImage: "square.stack")
                        }
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
}

#Preview {
    PreviewHost { ProjectsView() }
}
