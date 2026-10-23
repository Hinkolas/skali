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
                NavigationStack {
                    ProjectsView()
                }
            }
            if session.user.isAdmin {
                Tab("Settings", systemImage: "slider.horizontal.3", value: .settings) {
                    NavigationStack {
                        SettingsView()
                    }
                }
            } else {
                Tab("Account", systemImage: "person.crop.circle", value: .settings) {
                    NavigationStack {
                        AccountView(isRoot: true)
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
}
