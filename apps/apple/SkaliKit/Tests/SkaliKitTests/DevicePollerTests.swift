import Foundation
import Testing
@testable import SkaliKit

/// Hands out scripted poll answers and records the waits between them.
private actor Script {
    var answers: [Result<DevicePoll, APIError>]
    var waits: [Duration] = []
    var clock = Date(timeIntervalSince1970: 0)

    init(_ answers: [Result<DevicePoll, APIError>]) {
        self.answers = answers
    }

    func next() throws -> DevicePoll {
        try answers.removeFirst().get()
    }

    func wait(_ duration: Duration) {
        waits.append(duration)
        clock += TimeInterval(duration.components.seconds)
    }
}

struct DevicePollerTests {
    private func request(interval: Int = 5, ttl: TimeInterval = 600) -> DeviceRequest {
        DeviceRequest(
            deviceCode: "code", userCode: "BCDF-GHJK",
            expiresAt: Date(timeIntervalSince1970: ttl), interval: interval)
    }

    private func poller(
        _ script: Script, request: DeviceRequest, keepWaiting: @escaping @Sendable () -> Bool = { true }
    ) -> DeviceApprovalPoller {
        DeviceApprovalPoller(
            request: request,
            poll: { _ in try await script.next() },
            keepWaiting: keepWaiting,
            now: { Date(timeIntervalSince1970: 0) },
            sleep: { await script.wait($0) })
    }

    @Test func pollsUntilApprovedAndBacksOffOnSlowDown() async throws {
        let script = Script([
            .success(DevicePoll(status: "pending", session: nil)),
            .failure(.server(status: 429, code: "slow_down", message: "slow down")),
            .success(DevicePoll(status: "approved", session: nil)),
        ])
        let result = try await poller(script, request: request(interval: 1)).waitForApproval()
        #expect(result.status == "approved")
        // The interval never drops under five seconds, and grows by five on slow_down.
        #expect(await script.waits == [.seconds(5), .seconds(5), .seconds(10)])
    }

    @Test func deniedAndForgottenRequestsEndTheWait() async {
        let denied = Script([.success(DevicePoll(status: "denied", session: nil))])
        await #expect(throws: DeviceAuthorizationError.denied) {
            try await poller(denied, request: request()).waitForApproval()
        }
        let gone = Script([.failure(.server(status: 404, code: "not_found", message: "gone"))])
        await #expect(throws: DeviceAuthorizationError.expired) {
            try await poller(gone, request: request()).waitForApproval()
        }
    }

    @Test func stopsAfterOnePendingPollOnceTheBrowserIsGone() async {
        let script = Script([
            .success(DevicePoll(status: "pending", session: nil)),
            .success(DevicePoll(status: "approved", session: nil)),
        ])
        await #expect(throws: CancellationError.self) {
            try await poller(script, request: request(), keepWaiting: { false }).waitForApproval()
        }
        #expect(await script.waits.count == 1)
    }
}
