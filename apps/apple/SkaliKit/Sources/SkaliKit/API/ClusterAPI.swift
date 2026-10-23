import Foundation

/// The calls the apps make against one signed-in cluster. `APIClient`
/// implements it over HTTP; `DemoCluster` answers from fixtures for
/// previews and the demo mode.
public protocol ClusterAPI: Sendable {
    func currentSession() async throws -> CurrentSession
    func systemMeta() async throws -> SystemMeta
    /// Every project the caller can see, with summaries.
    func projects() async throws -> [Project]
    /// Admin only.
    func nodes() async throws -> [ClusterNode]
    /// Admin only.
    func databasePools() async throws -> [DatabasePool]
    /// Admin only; nil when no target is configured.
    func backupTarget() async throws -> BackupTarget?
    /// Admin only.
    func updateStatus() async throws -> UpdateStatus
    /// Admin only, and behind the recent sign-in gate.
    func saveUpdateSettings(_ settings: UpdateSettings) async throws -> UpdateStatus
    /// Opens a device request that re-confirms this session (sudo mode).
    func startDeviceReauth(clientLabel: String) async throws -> DeviceRequest
    func pollDevice(deviceCode: String) async throws -> DevicePoll
    /// Revokes this session on the cluster.
    func logout() async throws

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

    public func nodes() async throws -> [ClusterNode] {
        let response: NodesResponse = try await get("v1/nodes")
        return response.nodes
    }

    public func databasePools() async throws -> [DatabasePool] {
        let response: DatabasePoolsResponse = try await get("v1/system/database-pools")
        return response.pools
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

    public func saveUpdateSettings(_ settings: UpdateSettings) async throws -> UpdateStatus {
        try await put("v1/system/updates/settings", body: settings)
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

private struct DeviceStart: Encodable {
    let clientLabel: String
}

private struct DevicePollRequest: Encodable {
    let deviceCode: String
}
