import Foundation

/// The calls the apps make against one signed-in cluster. `APIClient`
/// implements it over HTTP; `DemoCluster` answers from fixtures for
/// previews and the demo mode.
public protocol ClusterAPI: Sendable {
    func currentSession() async throws -> CurrentSession
    func systemMeta() async throws -> SystemMeta
    /// Every project the caller can see, with summaries.
    func projects() async throws -> [Project]

    // MARK: One project

    /// The project's environments with their state and cached health.
    func environments(projectID: String) async throws -> [ProjectEnvironment]
    /// The project's draft; nil before anything submitted one.
    func projectDraft(projectID: String) async throws -> ProjectDraft?
    /// The newest footprint of every storage-consuming service, across
    /// the project's environments.
    func projectStorage(projectID: String) async throws -> [ServiceStorage]
    /// Tells when to re-read the project: `ready` on connect, `changed`
    /// after a change or a run, and `deleted` or `revoked` at the end.
    func projectChanges(projectID: String) -> AsyncThrowingStream<ProjectChange, Error>
    /// The status document first, then a new one whenever it changes.
    func environmentStatusUpdates(environmentID: String) -> AsyncThrowingStream<EnvironmentStatus, Error>
    func environmentMetrics(environmentID: String, window: NodeMetrics.Window) async throws -> EnvironmentMetrics
    /// Newest first.
    func runs(environmentID: String) async throws -> [Run]
    /// Recreates every application's pods with what is deployed; returns
    /// the run's id.
    func restartEnvironment(environmentID: String) async throws -> String
    /// Admin only.
    func nodes() async throws -> [ClusterNode]
    /// Admin only: CPU and memory per node over `window`.
    func nodeMetrics(window: NodeMetrics.Window) async throws -> NodeMetrics
    /// Admin only: the newest disk sample per node.
    func nodeStorage() async throws -> [NodeStorage]
    /// Admin only.
    func databasePools() async throws -> [DatabasePool]
    /// Admin only: one pool with its instances, databases and incidents.
    func databasePool(name: String) async throws -> DatabasePoolDetail
    /// Admin only: one pool's usage over `window`.
    func databasePoolMetrics(name: String, window: NodeMetrics.Window) async throws -> DatabasePoolMetrics
    /// Admin only; nil when no target is configured.
    func backupTarget() async throws -> BackupTarget?
    /// Admin only: the target with what its bucket holds and how backups
    /// run; nil when no target is configured, or the daemon has no summary
    /// (API-only mode, an older release).
    func backupSummary() async throws -> BackupSummary?
    /// Admin only, sudo: replaces the whole target.
    func saveBackupTarget(_ input: BackupTargetInput) async throws -> BackupTarget
    /// Admin only, sudo: forgets the target; snapshots in the bucket stay.
    func removeBackupTarget() async throws
    /// Admin only.
    func updateStatus() async throws -> UpdateStatus
    /// Admin only, and behind the recent sign-in gate.
    func saveUpdateSettings(_ settings: UpdateSettings) async throws -> UpdateStatus
    /// Admin only: checks the release feed now.
    func scanForUpdates() async throws -> UpdateStatus
    /// Admin only, sudo: moves every node to `version`, controllers first.
    func applyUpdate(version: String) async throws -> UpdateStatus
    /// Admin only, sudo: continues a failed update's remaining steps.
    func resumeUpdate() async throws -> UpdateStatus
    /// Opens a device request that re-confirms this session (sudo mode).
    func startDeviceReauth(clientLabel: String) async throws -> DeviceRequest
    func pollDevice(deviceCode: String) async throws -> DevicePoll
    /// Revokes this session on the cluster.
    func logout() async throws

    // MARK: Users (admin only; every change behind the sudo gate)

    /// Every account, oldest first.
    func users() async throws -> [User]
    func createUser(_ user: NewUser) async throws -> User
    func updateUser(id: String, _ patch: UserPatch) async throws -> User
    /// Deleting yourself or the last admin is refused (409).
    func deleteUser(id: String) async throws
    /// Sets a new password and signs the user out everywhere.
    func resetPassword(userID: String, newPassword: String) async throws

    // MARK: Account security

    /// The caller's unexpired sessions, newest first.
    func sessions() async throws -> [Session]
    /// Signs one of the caller's sessions out. Never behind the sudo gate.
    func revokeSession(id: String) async throws
    /// Behind the sudo gate; every other session of the account is revoked.
    func changePassword(current: String, new: String) async throws
    /// Behind the sudo gate: starts enrollment, which `confirmTwoFactor` completes.
    func enableTwoFactor() async throws -> TwoFactorEnrollment
    func confirmTwoFactor(code: String) async throws
    /// Behind the sudo gate.
    func disableTwoFactor() async throws
    /// Behind the sudo gate; the old codes stop working.
    func regenerateBackupCodes() async throws -> [String]
}

extension APIClient: ClusterAPI {
    public func currentSession() async throws -> CurrentSession {
        try await get("v1/auth/session")
    }

    public func systemMeta() async throws -> SystemMeta {
        try await get("v1/system/meta")
    }

    public func projects() async throws -> [Project] {
        let response: ProjectsResponse = try await get(
            "v1/projects", query: [URLQueryItem(name: "include", value: "summary")])
        return response.projects
    }

    public func environments(projectID: String) async throws -> [ProjectEnvironment] {
        let response: EnvironmentsResponse = try await get(
            "v1/projects/\(projectID)/environments", query: [URLQueryItem(name: "include", value: "summary")])
        return response.environments
    }

    public func projectDraft(projectID: String) async throws -> ProjectDraft? {
        do {
            let response: DraftResponse = try await get("v1/projects/\(projectID)/draft")
            return response.draft
        } catch let error as APIError where error.status == 404 {
            return nil
        }
    }

    public func projectStorage(projectID: String) async throws -> [ServiceStorage] {
        let response: ProjectStorageResponse = try await get("v1/projects/\(projectID)/storage")
        return response.services
    }

    public func projectChanges(projectID: String) -> AsyncThrowingStream<ProjectChange, Error> {
        let source = events("v1/projects/\(projectID)/stream")
        return AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    for try await event in source {
                        if let change = ProjectChange(rawValue: event.name) { continuation.yield(change) }
                    }
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    public func environmentStatusUpdates(environmentID: String) -> AsyncThrowingStream<EnvironmentStatus, Error> {
        .decoding(events("v1/environments/\(environmentID)/status/stream"), event: "status")
    }

    public func environmentMetrics(environmentID: String, window: NodeMetrics.Window) async throws -> EnvironmentMetrics {
        try await get(
            "v1/environments/\(environmentID)/metrics", query: [URLQueryItem(name: "window", value: window.rawValue)])
    }

    public func runs(environmentID: String) async throws -> [Run] {
        let response: RunsResponse = try await get("v1/environments/\(environmentID)/runs")
        return response.runs
    }

    public func restartEnvironment(environmentID: String) async throws -> String {
        let response: RunStarted = try await post("v1/environments/\(environmentID)/restart")
        return response.runId
    }

    public func nodes() async throws -> [ClusterNode] {
        let response: NodesResponse = try await get("v1/nodes")
        return response.nodes
    }

    public func nodeMetrics(window: NodeMetrics.Window) async throws -> NodeMetrics {
        try await get("v1/nodes/metrics", query: [URLQueryItem(name: "window", value: window.rawValue)])
    }

    public func nodeStorage() async throws -> [NodeStorage] {
        let response: NodesStorageResponse = try await get("v1/nodes/storage")
        return response.nodes
    }

    public func databasePools() async throws -> [DatabasePool] {
        let response: DatabasePoolsResponse = try await get("v1/system/database-pools")
        return response.pools
    }

    public func databasePool(name: String) async throws -> DatabasePoolDetail {
        let response: DatabasePoolResponse = try await get("v1/system/database-pools/\(name)")
        return response.pool
    }

    public func databasePoolMetrics(name: String, window: NodeMetrics.Window) async throws -> DatabasePoolMetrics {
        try await get(
            "v1/system/database-pools/\(name)/metrics",
            query: [URLQueryItem(name: "window", value: window.rawValue)])
    }

    public func backupTarget() async throws -> BackupTarget? {
        do {
            let response: BackupTargetResponse = try await get("v1/system/backup-target")
            return response.target
        } catch let error as APIError where error.status == 404 {
            return nil
        }
    }

    public func updateStatus() async throws -> UpdateStatus {
        try await get("v1/system/updates")
    }

    public func backupSummary() async throws -> BackupSummary? {
        do {
            return try await get("v1/system/backup-target/summary")
        } catch let error as APIError where error.status == 404 {
            return nil
        }
    }

    public func saveBackupTarget(_ input: BackupTargetInput) async throws -> BackupTarget {
        let response: BackupTargetResponse = try await put("v1/system/backup-target", body: input)
        return response.target
    }

    public func removeBackupTarget() async throws {
        try await delete("v1/system/backup-target")
    }

    public func saveUpdateSettings(_ settings: UpdateSettings) async throws -> UpdateStatus {
        try await put("v1/system/updates/settings", body: settings)
    }

    public func scanForUpdates() async throws -> UpdateStatus {
        try await post("v1/system/updates/scan")
    }

    public func applyUpdate(version: String) async throws -> UpdateStatus {
        try await post("v1/system/updates/apply", body: UpdateApply(version: version))
    }

    public func resumeUpdate() async throws -> UpdateStatus {
        try await post("v1/system/updates/resume")
    }

    public func startDeviceLogin(clientLabel: String) async throws -> DeviceRequest {
        try await post("v1/auth/device/requests", body: DeviceStart(clientLabel: clientLabel))
    }

    public func startDeviceReauth(clientLabel: String) async throws -> DeviceRequest {
        try await post("v1/auth/device/requests/reauth", body: DeviceStart(clientLabel: clientLabel))
    }

    public func pollDevice(deviceCode: String) async throws -> DevicePoll {
        try await post("v1/auth/device/token", body: DevicePollRequest(deviceCode: deviceCode))
    }

    public func logout() async throws {
        try await post("v1/auth/logout")
    }

    public func users() async throws -> [User] {
        let response: UsersResponse = try await get("v1/users")
        return response.users
    }

    public func createUser(_ user: NewUser) async throws -> User {
        let response: UserResponse = try await post("v1/users", body: user)
        return response.user
    }

    public func updateUser(id: String, _ patch: UserPatch) async throws -> User {
        let response: UserResponse = try await self.patch("v1/users/\(id)", body: patch)
        return response.user
    }

    public func deleteUser(id: String) async throws {
        try await delete("v1/users/\(id)")
    }

    public func resetPassword(userID: String, newPassword: String) async throws {
        try await post("v1/users/\(userID)/password", body: PasswordReset(newPassword: newPassword))
    }

    public func sessions() async throws -> [Session] {
        let response: SessionsResponse = try await get("v1/auth/sessions")
        return response.sessions
    }

    public func revokeSession(id: String) async throws {
        try await delete("v1/auth/sessions/\(id)")
    }

    public func changePassword(current: String, new: String) async throws {
        try await post("v1/auth/password", body: PasswordChange(currentPassword: current, newPassword: new))
    }

    public func enableTwoFactor() async throws -> TwoFactorEnrollment {
        try await post("v1/auth/2fa/enable")
    }

    public func confirmTwoFactor(code: String) async throws {
        try await post("v1/auth/2fa/confirm", body: TwoFactorCode(code: code))
    }

    public func disableTwoFactor() async throws {
        try await post("v1/auth/2fa/disable")
    }

    public func regenerateBackupCodes() async throws -> [String] {
        let response: BackupCodesResponse = try await post("v1/auth/2fa/backup-codes")
        return response.backupCodes
    }
}

private struct UsersResponse: Decodable {
    let users: [User]
}

private struct UserResponse: Decodable {
    let user: User
}

private struct PasswordReset: Encodable {
    let newPassword: String
}

private struct PasswordChange: Encodable {
    let currentPassword: String
    let newPassword: String
}

private struct TwoFactorCode: Encodable {
    let code: String
}

private struct BackupCodesResponse: Decodable {
    let backupCodes: [String]
}

private struct DatabasePoolResponse: Decodable {
    let pool: DatabasePoolDetail
}

private struct UpdateApply: Encodable {
    let version: String
}

private struct DeviceStart: Encodable {
    let clientLabel: String
}

private struct DevicePollRequest: Encodable {
    let deviceCode: String
}
