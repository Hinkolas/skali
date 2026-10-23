import Foundation

/// A cluster that answers from fixtures: SwiftUI previews and the demo
/// mode run against it, so neither needs a real cluster.
public struct DemoCluster: ClusterAPI {
    /// The signed-in user: an instance admin, or a member whom the admin
    /// routes refuse as a real cluster would.
    public let user: User

    public init(member: Bool = false) {
        user = member ? Self.member : Self.user
    }

    public static let address = ClusterAddress(userInput: "skali.example.com")!

    public static let user = User(
        id: "00000000-0000-0000-0000-000000000001", email: "ada@example.com",
        name: "Ada Lovelace", role: "admin", createProjects: true,
        twoFactorEnabled: false, createdAt: .now.addingTimeInterval(-200 * 86_400))

    public static let member = User(
        id: "00000000-0000-0000-0000-000000000002", email: "grace@example.com",
        name: "Grace Hopper", role: "member", createProjects: false,
        twoFactorEnabled: true, createdAt: .now.addingTimeInterval(-120 * 86_400))

    public var account: ClusterAccount {
        ClusterAccount(
            id: UUID(uuidString: "00000000-0000-0000-0000-00000000000A")!,
            address: Self.address, instanceID: "demo", instanceName: "example", user: user)
    }

    private func requireAdmin() throws {
        guard user.isAdmin else {
            throw APIError.server(status: 403, code: "forbidden", message: "admin role required")
        }
    }

    public func currentSession() async throws -> CurrentSession {
        CurrentSession(
            user: user,
            session: Session(
                id: "demo", expiresAt: .now.addingTimeInterval(30 * 86_400), ipAddress: nil,
                userAgent: nil, createdAt: .now, current: true))
    }

    public func systemMeta() async throws -> SystemMeta {
        SystemMeta(version: "v0.1.3-rc.8", name: "example", instanceId: "demo")
    }

    public func projects() async throws -> [Project] {
        Self.projects
    }

    public func nodes() async throws -> [ClusterNode] {
        try requireAdmin()
        return (1...11).map { ClusterNode(name: "node-\($0)", role: $0 <= 3 ? "server" : "agent", ready: true) }
    }

    public func databasePools() async throws -> [DatabasePool] {
        try requireAdmin()
        return [
            DatabasePool(name: "shared", engine: "postgres", major: 18, state: "ready"),
            DatabasePool(name: "analytics", engine: "postgres", major: 17, state: "ready"),
        ]
    }

    public func backupTarget() async throws -> BackupTarget? {
        try requireAdmin()
        return BackupTarget(name: "offsite", endpoint: "https://s3.example.com", bucket: "skali-backup")
    }

    public func updateStatus() async throws -> UpdateStatus {
        try requireAdmin()
        return UpdateStatus(
            installed: .init(version: "v0.1.3-rc.8"), summary: .init(state: "current"),
            channel: .beta, autoUpdate: false, lastCheckedAt: .now.addingTimeInterval(-3600))
    }

    public func saveUpdateSettings(_ settings: UpdateSettings) async throws -> UpdateStatus {
        try requireAdmin()
        return UpdateStatus(
            installed: .init(version: "v0.1.3-rc.8"), summary: .init(state: "current"),
            channel: settings.channel, autoUpdate: settings.autoUpdate)
    }

    public func startDeviceReauth(clientLabel: String) async throws -> DeviceRequest {
        DeviceRequest(deviceCode: "demo", userCode: "DEMO-CODE", expiresAt: .now.addingTimeInterval(600), interval: 5)
    }

    public func pollDevice(deviceCode: String) async throws -> DevicePoll {
        DevicePoll(status: "approved", session: nil)
    }

    public func logout() async throws {}

    public func sessions() async throws -> [Session] {
        func session(_ id: String, _ ua: String, ip: String?, days: Double, current: Bool = false) -> Session {
            let created = Date.now.addingTimeInterval(-days * 86_400)
            return Session(
                id: id, expiresAt: created.addingTimeInterval(30 * 86_400), ipAddress: ip,
                userAgent: ua, createdAt: created, current: current)
        }
        return [
            session("s1", SessionDevice.appUserAgent(version: "0.1.0", model: "iPhone", system: "iOS", systemVersion: "27.1"),
                    ip: "10.42.6.4", days: 0, current: true),
            session("s2", "skali/v0.1.3-rc.3 (studio-mac)", ip: "10.42.1.3", days: 3),
            session("s3", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/27.0 Safari/605.1.15",
                    ip: "10.42.1.3", days: 3.2),
            session("s4", "Mozilla/5.0 (Macintosh; Intel Mac OS X 15.0; rv:140.0) Gecko/20100101 Firefox/140.0",
                    ip: "10.42.0.9", days: 21),
            session("s5", "Mozilla/5.0 (iPhone; CPU iPhone OS 27_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/27.0 Mobile/15E148 Safari/604.1",
                    ip: "10.42.6.0", days: 26),
        ]
    }

    public func revokeSession(id: String) async throws {}

    public func changePassword(current: String, new: String) async throws {}

    public func enableTwoFactor() async throws -> TwoFactorEnrollment {
        TwoFactorEnrollment(
            secret: "JBSWY3DPEHPK3PXP",
            otpauthUri: "otpauth://totp/skali:ada@example.com?secret=JBSWY3DPEHPK3PXP&issuer=skali",
            backupCodes: ["4f2k-9xq1", "b7m3-p0zt", "q8w2-ld5c", "z1n6-r4ha", "e9u7-k3vy", "t5j8-c2gb"])
    }

    public func confirmTwoFactor(code: String) async throws {}

    public func disableTwoFactor() async throws {}

    public func regenerateBackupCodes() async throws -> [String] {
        try await enableTwoFactor().backupCodes
    }

    // MARK: Fixtures

    public static let projects: [Project] = [
        demo("almatar-web", environments: ["production"], apps: 1, days: 42),
        demo("birefnet-proxy", environments: ["staging"], apps: 1, databases: 1, buckets: 1, days: 19),
        demo("eplan-b1", environments: ["production"], apps: 1, databases: 1, days: 39),
        demo("eplan-b2", environments: ["production"], apps: 1, databases: 1, days: 39),
        demo("fuehrueber-web", environments: ["production"], apps: 1, days: 42),
        demo("go-lexware-automation", environments: ["production"], apps: 1, databases: 1, days: 41),
        demo("himmelreich", environments: ["staging", "production"], apps: 2, databases: 1, buckets: 2,
             days: 7, health: .progressing),
        demo("kaminholz-schulz-web", environments: ["production"], apps: 1, days: 42),
        demo("lilienthal-site", environments: ["production"], apps: 1, buckets: 1, days: 12),
        demo("maja", environments: ["preview"], apps: 1, days: 2, health: .degraded),
        demo("nordlicht-api", environments: ["production", "staging"], apps: 3, databases: 2, days: 1),
        demo("orbit", environments: [], days: 90),
    ]

    private static func demo(
        _ name: String, environments: [String], apps: Int = 0, databases: Int = 0,
        buckets: Int = 0, days: Double, health: ServiceHealth = .healthy
    ) -> Project {
        let activity = Date.now.addingTimeInterval(-days * 86_400 - 600)
        return Project(
            id: UUID().uuidString, name: name, createdAt: activity.addingTimeInterval(-30 * 86_400),
            updatedAt: activity, access: ProjectAccess(role: "admin", member: true),
            summary: ProjectSummary(
                environments: environments.enumerated().map { index, environment in
                    SummaryEnvironment(
                        id: "\(name)-\(environment)", name: environment,
                        priority: environment == "production" ? "high" : "normal",
                        health: index == 0 ? health : .healthy)
                },
                serviceCounts: ServiceCounts(applications: apps, databases: databases, buckets: buckets),
                updatedAt: activity))
    }
}
