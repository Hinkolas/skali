import SwiftUI

enum AppTab: String, Hashable {
    case projects, settings, search
}

/// The app's shell: a tab bar on iPhone, a sidebar-adaptable tab view on
/// iPad, with search as its own tab the way the system apps do it.
struct MainTabView: View {
    @State private var tab: AppTab = .initial

    var body: some View {
        TabView(selection: $tab) {
            Tab("Projects", systemImage: "square.grid.2x2", value: .projects) {
                NavigationStack {
                    ProjectsView()
                }
            }
            Tab("Settings", systemImage: "slider.horizontal.3", value: .settings) {
                NavigationStack {
                    SettingsView()
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

private extension AppTab {
    /// Debug builds open the tab named by `-SkaliTab <tab>` (screenshots, UI work).
    static var initial: AppTab {
        #if DEBUG
        UserDefaults.standard.string(forKey: "SkaliTab").flatMap(AppTab.init(rawValue:)) ?? .projects
        #else
        .projects
        #endif
    }
}
