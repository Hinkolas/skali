import SkaliKit
import SwiftUI

/// Sign-in until a cluster is selected, the app after.
struct RootView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Group {
            if let session = model.session {
                MainTabView()
                    .environment(session)
                    .id(session.account.id)
            } else {
                NavigationStack {
                    SignInView()
                }
            }
        }
        .animation(.default, value: model.session?.account.id)
    }
}
