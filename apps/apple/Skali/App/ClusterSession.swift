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

    /// Every account on the cluster (admins only), oldest first.
    private(set) var users: [User]?

    /// The newest usage sample per pool name.
    private(set) var poolUsage: [String: DatabasePoolMetrics.Current] = [:]
    /// Each opened pool's instances, databases and incidents, by name.
    private(set) var poolDetails: [String: DatabasePoolDetail] = [:]

    /// Current CPU, memory and disk per node name.
    private(set) var nodeUsage: [String: NodeUsage] = [:]

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
        async let users = try? api.users()
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
        if let users = await users { self.users = users }
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

    /// The nodes screens: the nodes, their current usage (the newest
    /// bucket of the shortest window) and disk, and the update document,
    /// which carries each node's host agent version.
    func loadNodes() async {
        async let nodes = try? api.nodes()
        async let metrics = try? api.nodeMetrics(window: .hour)
        async let storage = try? api.nodeStorage()
        async let updates = try? api.updateStatus()
        if let nodes = await nodes { system.nodes = nodes }
        let metricsValue = await metrics
        let storageValue = await storage
        if metricsValue != nil || storageValue != nil {
            nodeUsage = NodeUsage.byNode(metrics: metricsValue, storage: storageValue)
        }
        if let updates = await updates { system.updates = updates }
    }

    /// The pools and their newest usage sample (one metrics read per pool;
    /// a cluster runs a handful).
    func loadPools() async {
        do {
            let pools = try await api.databasePools()
            system.pools = pools
            let api = self.api
            let samples = await withTaskGroup(of: (String, DatabasePoolMetrics.Current?).self) { group in
                for pool in pools {
                    group.addTask { (pool.name, try? await api.databasePoolMetrics(name: pool.name, window: .hour).current) }
                }
                var samples: [String: DatabasePoolMetrics.Current] = [:]
                for await (name, current) in group { samples[name] = current }
                return samples
            }
            poolUsage = samples
        } catch {
            _ = handleUnauthorized(error)
        }
    }

    /// One pool with its instances and databases.
    func loadPool(_ name: String) async {
        async let detail = try? api.databasePool(name: name)
        async let metrics = try? api.databasePoolMetrics(name: name, window: .hour)
        if let detail = await detail { poolDetails[name] = detail }
        if let current = await metrics?.current { poolUsage[name] = current }
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

    func loadUsers() async {
        do {
            users = try await api.users()
        } catch {
            _ = handleUnauthorized(error)
        }
    }

    // MARK: Changes

    func createUser(_ new: NewUser) async throws -> User {
        let user = try await gated { try await self.api.createUser(new) }
        users?.append(user)
        return user
    }

    func updateUser(id: String, _ patch: UserPatch) async throws {
        let user = try await gated { try await self.api.updateUser(id: id, patch) }
        if let index = users?.firstIndex(where: { $0.id == id }) { users?[index] = user }
        if user.id == self.user.id { self.user = user }
    }

    func deleteUser(id: String) async throws {
        try await gated { try await self.api.deleteUser(id: id) }
        users?.removeAll { $0.id == id }
    }

    /// Signs the user out everywhere.
    func resetPassword(userID: String, newPassword: String) async throws {
        try await gated { try await self.api.resetPassword(userID: userID, newPassword: newPassword) }
    }

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
