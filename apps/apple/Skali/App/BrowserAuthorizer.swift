import AuthenticationServices
import SkaliKit
import Synchronization
import UIKit

/// Shows a device request's approval page in a browser sheet and waits for
/// the approval by polling, as the CLI does.
///
/// The sheet shares Safari's cookies, so someone already signed in to the
/// Studio only has to press Approve, and someone who is not gets Safari's
/// password autofill for the cluster's own domain (which the app could
/// never offer: every cluster lives on a different host). The page never
/// hands a credential back to the app; the session arrives in the poll.
final class BrowserAuthorizer: NSObject, ASWebAuthenticationPresentationContextProviding {
    func approve(
        _ request: DeviceRequest, at address: ClusterAddress,
        poll: @escaping DeviceApprovalPoller.Poll
    ) async throws -> DevicePoll {
        let sheetClosed = Mutex(false)
        // The approval page does not redirect yet, so the callback only fires
        // when the person closes the sheet. A Studio that sends the browser to
        // skali://device/approved would close it the moment Approve is pressed.
        let session = ASWebAuthenticationSession(
            url: address.verificationURL(userCode: request.userCode),
            callback: .customScheme("skali")
        ) { @Sendable _, _ in
            sheetClosed.withLock { $0 = true }
        }
        session.presentationContextProvider = self
        session.prefersEphemeralWebBrowserSession = false
        guard session.start() else { throw CancellationError() }
        defer { session.cancel() }

        let poller = DeviceApprovalPoller(
            request: request, poll: poll,
            keepWaiting: { !sheetClosed.withLock { $0 } })
        return try await poller.waitForApproval()
    }

    func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        let windows = scenes.flatMap(\.windows)
        if let window = windows.first(where: \.isKeyWindow) ?? windows.first {
            return window
        }
        guard let scene = scenes.first else {
            fatalError("No window scene to present the sign-in sheet on")
        }
        return ASPresentationAnchor(windowScene: scene)
    }
}
