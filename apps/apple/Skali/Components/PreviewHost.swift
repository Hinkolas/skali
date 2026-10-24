import SkaliKit
import SwiftUI

/// Wraps a screen for SwiftUI previews with the demo cluster's session.
struct PreviewHost<Content: View>: View {
    @State private var model: AppModel
    private let content: Content

    init(demo: DemoCluster = DemoCluster(), @ViewBuilder content: () -> Content) {
        _model = State(initialValue: AppModel(demo: demo))
        self.content = content()
    }

    var body: some View {
        if let session = model.session {
            NavigationStack { content }
                .environment(model)
                .environment(session)
        }
    }
}
