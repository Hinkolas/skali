import Foundation
import Testing
@testable import SkaliKit

/// Decodes every read the apps make against a real daemon. Opt-in: set
/// SKALI_LIVE_URL (e.g. http://localhost:7070) and SKALI_LIVE_TOKEN (an
/// admin session token, e.g. from `skali remote token`).
@Suite(.enabled(if: LiveCluster.client != nil, "set SKALI_LIVE_URL and SKALI_LIVE_TOKEN"))
struct LiveClusterTests {
    let api = LiveCluster.client!

    @Test func readsTheSessionAndMeta() async throws {
        let session = try await api.currentSession()
        #expect(!session.user.email.isEmpty)
        let meta = try await api.systemMeta()
        #expect(!meta.version.isEmpty)
    }

    @Test func readsTheSessionList() async throws {
        let sessions = try await api.sessions()
        #expect(sessions.contains { $0.isCurrent })
    }

    @Test func readsProjectsWithSummaries() async throws {
        let projects = try await api.projects()
        #expect(projects.allSatisfy { $0.summary != nil })
    }

    @Test func readsTheAdminOverview() async throws {
        _ = try await api.nodes()
        _ = try await api.databasePools()
        _ = try await api.backupTarget()
        let updates = try await api.updateStatus()
        #expect(!updates.installed.version.isEmpty)
    }
}

enum LiveCluster {
    static let client: APIClient? = {
        let environment = ProcessInfo.processInfo.environment
        guard let url = environment["SKALI_LIVE_URL"], let token = environment["SKALI_LIVE_TOKEN"],
              let address = ClusterAddress(userInput: url)
        else { return nil }
        return APIClient(address: address, token: token)
    }()
}
