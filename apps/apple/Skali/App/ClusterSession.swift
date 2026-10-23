import Foundation
import Observation
import SkaliKit

/// The data the app shows for the selected cluster, loaded on demand and
/// kept while the account stays selected.
@Observable
final class ClusterSession {
    let account: ClusterAccount
    let api: any ClusterAPI

    private(set) var user: User
    private(set) var meta: SystemMeta?

    private(set) var projects: [Project]?
    private(set) var projectsError: String?

    private(set) var system = SystemOverview()

    private let reauthenticate: () async throws -> Void
    private let onUnauthorized: () -> Void

    init(
        account: ClusterAccount, api: any ClusterAPI,
        reauthenticate: @escaping () async throws -> Void,
        onUnauthorized: @escaping () -> Void
    ) {
        self.account = account
        self.api = api
        self.user = account.user
        self.reauthenticate = reauthenticate
        self.onUnauthorized = onUnauthorized
    }

    // MARK: Loading

    func loadProjects() async {
        async let meta = try? api.systemMeta()
        async let session = try? api.currentSession()
        do {
            projects = try await api.projects()
            projectsError = nil
        } catch {
            if handleUnauthorized(error) { return }
            projectsError = error.localizedDescription
        }
        if let meta = await meta { self.meta = meta }
        if let session = await session { user = session.user }
    }

    /// The admin overview behind the Settings rows. Every part degrades on
    /// its own: a failed read shows as unavailable, not as an error page.
    func loadSystem() async {
        async let meta = try? api.systemMeta()
        guard user.isAdmin else {
            if let meta = await meta { self.meta = meta }
            return
        }
        async let nodes = try? api.nodes()
        async let pools = try? api.databasePools()
        async let updates = try? api.updateStatus()
        async let backup = Result { try await api.backupTarget() }
        let target: SystemOverview.Backup = switch await backup {
        case .success(let target?): .set(target)
        case .success(nil): .none
        case .failure: .unavailable
        }
        system = SystemOverview(
            nodes: await nodes, pools: await pools, updates: await updates, backup: target)
        if let meta = await meta { self.meta = meta }
    }

    // MARK: Changes

    /// Saves the update settings, confirming the session in the browser
    /// first when the daemon asks for a recent sign-in.
    func saveUpdateSettings(_ settings: UpdateSettings) async throws {
        do {
            system.updates = try await api.saveUpdateSettings(settings)
        } catch let error as APIError where error.isReauthRequired {
            try await reauthenticate()
            system.updates = try await api.saveUpdateSettings(settings)
        } catch {
            _ = handleUnauthorized(error)
            throw error
        }
    }

    /// A revoked or expired session sends the app back to sign-in.
    private func handleUnauthorized(_ error: Error) -> Bool {
        guard let error = error as? APIError, error.isUnauthorized else { return false }
        onUnauthorized()
        return true
    }
}

/// What the Settings rows summarize for an instance admin.
struct SystemOverview {
    enum Backup {
        case unknown, unavailable, none
        case set(BackupTarget)
    }

    var nodes: [ClusterNode]?
    var pools: [DatabasePool]?
    var updates: UpdateStatus?
    var backup: Backup = .unknown
}

private extension Result where Failure == any Error {
    init(catching body: () async throws -> Success) async {
        do { self = .success(try await body()) } catch { self = .failure(error) }
    }
}
