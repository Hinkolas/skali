import Foundation

/// Why a browser sign-in ended without a session.
public enum DeviceAuthorizationError: Error, Sendable, Equatable {
    /// Someone pressed Deny in the Studio.
    case denied
    /// The request ran out (ten minutes), or the cluster forgot it.
    case expired
}

extension DeviceAuthorizationError: LocalizedError {
    public var errorDescription: String? {
        switch self {
        case .denied: "The sign-in request was denied."
        case .expired: "The sign-in request expired. Try again."
        }
    }
}

/// Waits for the Studio to approve a device request, the same way the CLI
/// does (cmd/skali/devicelogin.go): poll no faster than the server's
/// interval (and never under five seconds), back off by five seconds on
/// `slow_down`, and stop once the request has expired.
///
/// The browser never hands the app anything; the session only ever
/// arrives in a poll response, exactly once.
public struct DeviceApprovalPoller: Sendable {
    public typealias Poll = @Sendable (_ deviceCode: String) async throws -> DevicePoll

    private let request: DeviceRequest
    private let poll: Poll
    private let now: @Sendable () -> Date
    private let sleep: @Sendable (Duration) async throws -> Void
    private let keepWaiting: @Sendable () -> Bool

    /// `keepWaiting` is asked after every pending answer: returning false
    /// ends the wait with a `CancellationError`. The app returns false once
    /// the browser sheet is gone, so a person who approved and then closed
    /// the sheet still gets the one poll that picks the approval up.
    public init(
        request: DeviceRequest,
        poll: @escaping Poll,
        keepWaiting: @escaping @Sendable () -> Bool = { true },
        now: @escaping @Sendable () -> Date = { Date() },
        sleep: @escaping @Sendable (Duration) async throws -> Void = { try await Task.sleep(for: $0) }
    ) {
        self.request = request
        self.poll = poll
        self.keepWaiting = keepWaiting
        self.now = now
        self.sleep = sleep
    }

    /// Polls until the request is approved, and returns the approving poll
    /// (with the new session for a login, without one for a reauth).
    public func waitForApproval() async throws -> DevicePoll {
        var interval = max(request.interval, 5)
        let deadline = request.expiresAt.addingTimeInterval(TimeInterval(interval))
        while now() < deadline {
            try await sleep(.seconds(interval))
            let result: DevicePoll
            do {
                result = try await poll(request.deviceCode)
            } catch let error as APIError where error.code == "slow_down" {
                interval += 5
                continue
            } catch let error as APIError where error.status == 404 {
                throw DeviceAuthorizationError.expired
            }
            switch result.status {
            case "approved": return result
            case "denied": throw DeviceAuthorizationError.denied
            case "expired": throw DeviceAuthorizationError.expired
            default: if !keepWaiting() { throw CancellationError() }
            }
        }
        throw DeviceAuthorizationError.expired
    }
}
