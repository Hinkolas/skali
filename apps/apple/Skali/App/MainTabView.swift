import SkaliKit
import SwiftUI

enum AppTab: String, Hashable {
    case projects, settings, search
}

/// The app's shell: Projects, then Settings for instance admins or the
/// account for members (the Settings root is cluster administration, which
/// members cannot open), and search as its own tab.
struct MainTabView: View {
    @Environment(AppModel.self) private var model
    @Environment(ClusterSession.self) private var session

    var body: some View {
        @Bindable var model = model
        TabView(selection: $model.selectedTab) {
            Tab("Projects", systemImage: "square.grid.2x2", value: .projects) {
                NavigationStack(path: $model.projectsPath) {
                    ProjectsView()
                }
            }
            if session.user.isAdmin {
                Tab("Settings", systemImage: "slider.horizontal.3", value: .settings) {
                    NavigationStack(path: $model.settingsPath) {
                        SettingsView()
                    }
                }
            } else {
                Tab("Account", systemImage: "person.crop.circle", value: .settings) {
                    NavigationStack {
                        AccountView()
                    }
                }
            }
            Tab(value: .search, role: .search) {
                NavigationStack {
                    SearchView()
                }
            }
        }
        .tabBarMinimizeBehavior(.onScrollDown)
    }
}

extension AppTab {
    /// Debug builds open the tab named by `-SkaliTab <tab>` (screenshots, UI work).
    static var initial: AppTab {
        #if DEBUG
        UserDefaults.standard.string(forKey: "SkaliTab").flatMap(AppTab.init(rawValue:)) ?? .projects
        #else
        .projects
        #endif
    }

    /// Debug builds open the project named by `-SkaliProject <name>` once
    /// the list has loaded.
    static var initialProject: String? {
        #if DEBUG
        UserDefaults.standard.string(forKey: "SkaliProject")
        #else
        nil
        #endif
    }

    /// Debug builds go on to the project page named by
    /// `-SkaliProjectRoute <page>`: `deployments`, `backups`, or a service
    /// as `application:<key>`, `database:<key>` or `bucket:<key>`.
    static var initialProjectRoute: ProjectRoute? {
        #if DEBUG
        guard let argument = UserDefaults.standard.string(forKey: "SkaliProjectRoute") else { return nil }
        switch argument {
        case "deployments": return .deployments
        case "backups": return .backups
        default:
            let parts = argument.split(separator: ":", maxSplits: 1).map(String.init)
            guard parts.count == 2, let kind = ServiceKind(rawValue: parts[0]) else { return nil }
            return .service(kind, parts[1])
        }
        #else
        nil
        #endif
    }

    /// Debug builds show the project's environment named by
    /// `-SkaliEnvironment <name>`.
    static var initialEnvironment: String? {
        #if DEBUG
        UserDefaults.standard.string(forKey: "SkaliEnvironment")
        #else
        nil
        #endif
    }

    /// Debug builds open the overview's sheet named by
    /// `-SkaliProjectSheet <sheet>`: `promote`, `redeploy`, or `accept`
    /// for an environment an incomplete restore holds.
    static var initialProjectSheet: String? {
        #if DEBUG
        UserDefaults.standard.string(forKey: "SkaliProjectSheet")
        #else
        nil
        #endif
    }

    /// Debug builds push the Settings screen named by `-SkaliRoute <route>`.
    static var initialSettingsPath: [SettingsRoute] {
        #if DEBUG
        // `node:<name>`, `pool:<name>` and `user:<id>` push their list first.
        guard let argument = UserDefaults.standard.string(forKey: "SkaliRoute"),
              let route = SettingsRoute(argument: argument)
        else { return [] }
        switch route {
        case .node: return [.nodes, route]
        case .pool: return [.databases, route]
        case .user: return [.users, route]
        default: return [route]
        }
        #else
        []
        #endif
    }
}
