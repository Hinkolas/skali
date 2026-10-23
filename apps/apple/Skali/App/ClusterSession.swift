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
    /// The last update poll failed (the control plane restarts mid-update);
    /// the last document stays on screen.
    private(set) var updatesDisconnected = false

    /// The account's sessions on this cluster, newest first.
    private(set) var sessions: [Session]?
    private(set) var sessionsError: String?

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

    /// The account screen: the user (2FA state can change elsewhere) and
    /// the session list.
    func loadAccount() async {
        async let current = try? api.currentSession()
        do {
            sessions = try await api.sessions()
            sessionsError = nil
        } catch {
            if handleUnauthorized(error) { return }
            sessionsError = error.localizedDescription
        }
        if let current = await current { user = current.user }
    }

    /// Re-reads the update document, keeping the last one when the cluster
    /// does not answer.
    func refreshUpdates() async {
        do {
            system.updates = try await api.updateStatus()
            updatesDisconnected = false
        } catch is CancellationError {
        } catch {
            if handleUnauthorized(error) { return }
            updatesDisconnected = true
        }
    }

    // MARK: Changes

    func scanForUpdates() async throws {
        system.updates = try await gated { try await self.api.scanForUpdates() }
    }

    /// Starts (or finishes) the update to the summary's target release.
    func applyUpdate(version: String) async throws {
        system.updates = try await gated { try await self.api.applyUpdate(version: version) }
    }

    func resumeUpdate() async throws {
        system.updates = try await gated { try await self.api.resumeUpdate() }
    }

    func saveUpdateSettings(_ settings: UpdateSettings) async throws {
        system.updates = try await gated { try await self.api.saveUpdateSettings(settings) }
    }

    /// Signs another session out; revoking is never sudo-gated.
    func revoke(_ session: Session) async throws {
        try await gated { try await self.api.revokeSession(id: session.id) }
        sessions?.removeAll { $0.id == session.id }
    }

    /// The daemon signs every other session out, so the list is re-read.
    func changePassword(current: String, new: String) async throws {
        try await gated { try await self.api.changePassword(current: current, new: new) }
        await loadAccount()
    }

    func enableTwoFactor() async throws -> TwoFactorEnrollment {
        try await gated { try await self.api.enableTwoFactor() }
    }

    func confirmTwoFactor(code: String) async throws {
        try await gated { try await self.api.confirmTwoFactor(code: code) }
        await loadAccount()
    }

    func disableTwoFactor() async throws {
        try await gated { try await self.api.disableTwoFactor() }
        await loadAccount()
    }

    func regenerateBackupCodes() async throws -> [String] {
        try await gated { try await self.api.regenerateBackupCodes() }
    }

    /// Runs a call, confirming the session in the browser first when the
    /// daemon asks for a recent sign-in (403 `reauth_required`), then once more.
    private func gated<Result>(_ call: () async throws -> Result) async throws -> Result {
        do {
            do {
                return try await call()
            } catch let error as APIError where error.isReauthRequired {
                try await reauthenticate()
                return try await call()
            }
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
