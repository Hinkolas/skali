import SkaliKit
import SwiftUI

/// Wraps a screen for SwiftUI previews with the demo cluster's session.
struct PreviewHost<Content: View>: View {
    @State private var model = AppModel(demo: true)
    @ViewBuilder var content: Content

    var body: some View {
        if let session = model.session {
            NavigationStack { content }
                .environment(model)
                .environment(session)
        }
    }
}
