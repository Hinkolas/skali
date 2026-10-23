import Foundation

/// A cluster that answers from fixtures: SwiftUI previews and the demo
/// mode run against it, so neither needs a real cluster.
public struct DemoCluster: ClusterAPI {
    public init() {}

    public static let address = ClusterAddress(userInput: "skali.example.com")!

    public static let user = User(
        id: "00000000-0000-0000-0000-000000000001", email: "ada@example.com",
        name: "Ada Lovelace", role: "admin", createProjects: true,
        twoFactorEnabled: true, createdAt: .now.addingTimeInterval(-200 * 86_400))

    public static let account = ClusterAccount(
        id: UUID(uuidString: "00000000-0000-0000-0000-00000000000A")!,
        address: address, instanceID: "demo", instanceName: "example", user: user)

    public func currentSession() async throws -> CurrentSession {
        CurrentSession(
            user: Self.user,
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
        (1...11).map { ClusterNode(name: "node-\($0)", role: $0 <= 3 ? "server" : "agent", ready: true) }
    }

    public func databasePools() async throws -> [DatabasePool] {
        [
            DatabasePool(name: "shared", engine: "postgres", major: 18, state: "ready"),
            DatabasePool(name: "analytics", engine: "postgres", major: 17, state: "ready"),
        ]
    }

    public func backupTarget() async throws -> BackupTarget? {
        BackupTarget(name: "offsite", endpoint: "https://s3.example.com", bucket: "skali-backup")
    }

    public func updateStatus() async throws -> UpdateStatus {
        UpdateStatus(
            installed: .init(version: "v0.1.3-rc.8"), summary: .init(state: "current"),
            channel: .beta, autoUpdate: false, lastCheckedAt: .now.addingTimeInterval(-3600))
    }

    public func saveUpdateSettings(_ settings: UpdateSettings) async throws -> UpdateStatus {
        UpdateStatus(
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
