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

    /// Every read of a project's overview, on the first project with an
    /// environment the caller can open; the status stream's first event
    /// is the full document.
    @Test func readsAProjectOverview() async throws {
        for project in try await api.projects() {
            let environments = try await api.environments(projectID: project.id)
            guard let environment = environments.first(where: { !$0.isLocked && $0.state == "active" }) else {
                continue
            }
            _ = try await api.projectDraft(projectID: project.id)
            _ = try await api.projectStorage(projectID: project.id)
            _ = try await api.environmentMetrics(environmentID: environment.id, window: .day)
            let runs = try await api.runs(environmentID: environment.id)
            if let run = runs.first {
                let tree = try await api.run(id: run.id)
                #expect(tree.run.id == run.id)
                if let step = tree.steps.first {
                    _ = try await api.stepLogs(stepID: step.id, after: nil)
                }
            }
            do {
                _ = try await api.snapshots(projectID: project.id)
            } catch let error as APIError where error.status == 503 || (error.status == 404 && error.code == nil) {
                // No backup target, or an API-only daemon without the route.
            }
            #expect(try await api.userDirectory().isEmpty == false)
            var statuses = api.environmentStatusUpdates(environmentID: environment.id).makeAsyncIterator()
            let status = try await statuses.next()
            #expect(status?.environmentId == environment.id)
            var changes = api.projectChanges(projectID: project.id).makeAsyncIterator()
            #expect(try await changes.next() == .ready)
            return
        }
    }

    @Test func readsTheAdminOverview() async throws {
        _ = try await api.nodes()
        let users = try await api.users()
        #expect(users.contains { $0.isAdmin })
        _ = try await api.nodeMetrics(window: .hour)
        _ = try await api.nodeStorage()
        let pools = try await api.databasePools()
        if let pool = pools.first {
            _ = try await api.databasePool(name: pool.name)
            _ = try await api.databasePoolMetrics(name: pool.name, window: .hour)
        }
        _ = try await api.backupTarget()
        // Nil without a target, and on an API-only daemon (no summary route).
        _ = try await api.backupSummary()
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
