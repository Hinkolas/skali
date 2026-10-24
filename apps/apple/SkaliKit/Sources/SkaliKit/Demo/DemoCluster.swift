import Foundation

/// A cluster that answers from fixtures: SwiftUI previews and the demo
/// mode run against it, so neither needs a real cluster.
public struct DemoCluster: ClusterAPI {
    /// The signed-in user: an instance admin, or a member whom the admin
    /// routes refuse as a real cluster would.
    public let user: User
    private let updates: DemoUpdates
    private let directory: DemoUsers
    let backups: DemoBackups
    /// Schedules, snapshots and runs the demo changed or started.
    let activity = DemoActivity()

    /// `update` picks where the cluster's update stands at launch; an
    /// update the demo runs advances with the clock.
    public init(member: Bool = false, update: DemoUpdates.Scenario = .current, backups: DemoBackups.Scenario = .configured) {
        user = member ? Self.member : Self.user
        updates = DemoUpdates(scenario: update)
        directory = DemoUsers(signedIn: member ? Self.member : Self.user)
        self.backups = DemoBackups(scenario: backups)
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
        DemoProjects.fixtures.map { DemoProjects.project($0, environments: environments($0)) }
    }

    public func nodes() async throws -> [ClusterNode] {
        try requireAdmin()
        return Self.nodeNames.enumerated().map { index, node in
            ClusterNode(
                name: node.name, role: node.controller ? "server" : "agent",
                capabilities: node.capabilities, arch: "arm64", os: "linux",
                kubeletVersion: "v1.36.3+k3s1", ready: true,
                internalIp: "10.42.\(node.controller ? 0 : 1).\(index + 2)",
                externalIp: index == 0 ? "203.0.113.10" : nil,
                lastHeartbeat: .now.addingTimeInterval(-Double(4 + index % 7)),
                memoryAllocatableBytes: Self.memory(node))
        }
    }

    public func nodeMetrics(window: NodeMetrics.Window) async throws -> NodeMetrics {
        try requireAdmin()
        let (count, step): (Int, Int) = switch window {
        case .hour: (60, 60)
        case .day: (96, 900)
        case .week: (168, 3600)
        }
        let end = Date.now
        let timestamps = (0..<count).map { end.addingTimeInterval(-Double((count - 1 - $0) * step)) }
        let series = Self.nodeNames.enumerated().map { index, node in
            let cpu = Self.cores(node) * 1000
            let memory = Self.memory(node)
            // A steady base per node with a daily swell and some noise.
            let base = 0.08 + Double((index * 37) % 23) / 100
            let values: [Double] = timestamps.map { time in
                let seconds = time.timeIntervalSince1970
                let day = sin(seconds / 86_400 * 2 * .pi + Double(index))
                // Slow swings only, so no window's buckets alias them into a zigzag.
                let noise = sin(seconds / 21_600 * 2 * .pi + Double(index * 3)) * 0.025
                    + sin(seconds / 7_200 * 2 * .pi + Double(index)) * 0.015
                return min(0.95, max(0.02, base + day * 0.06 + noise))
            }
            return NodeMetrics.Series(
                name: node.name, cpuAllocatableMillicores: cpu, memoryAllocatableBytes: memory,
                cpuMillicores: values.map { Int($0 * Double(cpu)) },
                memoryBytes: values.map { Int64((0.3 + $0 * 0.8) * Double(memory)) })
        }
        return NodeMetrics(window: window, stepSeconds: step, timestamps: timestamps, nodes: series)
    }

    public func nodeStorage() async throws -> [NodeStorage] {
        try requireAdmin()
        return Self.nodeNames.enumerated().map { index, node in
            let capacity: Int64 = 80 * 1 << 30
            let used = Int64(Double(capacity) * (0.18 + Double((index * 13) % 30) / 100))
            return NodeStorage(
                name: node.name, sampledAt: .now.addingTimeInterval(-120), capacityBytes: capacity,
                usedBytes: used, availableBytes: capacity - used)
        }
    }

    static let nodeNames: [(name: String, controller: Bool, capabilities: [String])] =
        (1...3).map { ("skali-core-0\($0)", true, $0 == 1 ? ["edge"] : []) }
        + (1...8).map { ("skali-app-0\($0)", false, $0 == 1 ? ["builder"] : $0 == 5 ? ["database"] : []) }

    private static func cores(_ node: (name: String, controller: Bool, capabilities: [String])) -> Int {
        node.controller ? 2 : 4
    }

    private static func memory(_ node: (name: String, controller: Bool, capabilities: [String])) -> Int64 {
        (node.controller ? 4 : 8) * 1 << 30
    }

    public func databasePools() async throws -> [DatabasePool] {
        try requireAdmin()
        return DemoPools.pools
    }

    public func databasePool(name: String) async throws -> DatabasePoolDetail {
        try requireAdmin()
        guard let detail = DemoPools.detail(name) else {
            throw APIError.server(status: 404, code: "not_found", message: "database pool not found")
        }
        return detail
    }

    public func databasePoolMetrics(name: String, window: NodeMetrics.Window) async throws -> DatabasePoolMetrics {
        try requireAdmin()
        return DemoPools.metrics(name, window: window)
    }

    public func backupTarget() async throws -> BackupTarget? {
        try requireAdmin()
        return backups.current()
    }

    public func backupSummary() async throws -> BackupSummary? {
        try requireAdmin()
        return backups.summary()
    }

    public func saveBackupTarget(_ input: BackupTargetInput) async throws -> BackupTarget {
        try requireAdmin()
        return try backups.save(input)
    }

    public func removeBackupTarget() async throws {
        try requireAdmin()
        try backups.remove()
    }

    public func updateStatus() async throws -> UpdateStatus {
        try requireAdmin()
        return updates.status()
    }

    public func saveUpdateSettings(_ settings: UpdateSettings) async throws -> UpdateStatus {
        try requireAdmin()
        updates.save(settings)
        return updates.status()
    }

    public func scanForUpdates() async throws -> UpdateStatus {
        try requireAdmin()
        updates.scan()
        return updates.status()
    }

    public func applyUpdate(version: String) async throws -> UpdateStatus {
        try requireAdmin()
        updates.start()
        return updates.status()
    }

    public func resumeUpdate() async throws -> UpdateStatus {
        try requireAdmin()
        updates.start()
        return updates.status()
    }

    public func startDeviceReauth(clientLabel: String) async throws -> DeviceRequest {
        DeviceRequest(deviceCode: "demo", userCode: "DEMO-CODE", expiresAt: .now.addingTimeInterval(600), interval: 5)
    }

    public func pollDevice(deviceCode: String) async throws -> DevicePoll {
        DevicePoll(status: "approved", session: nil)
    }

    public func logout() async throws {}

    public func users() async throws -> [User] {
        try requireAdmin()
        return directory.list()
    }

    public func createUser(_ new: NewUser) async throws -> User {
        try requireAdmin()
        return try directory.create(new)
    }

    public func updateUser(id: String, _ patch: UserPatch) async throws -> User {
        try requireAdmin()
        return try directory.update(id: id, patch)
    }

    public func deleteUser(id: String) async throws {
        try requireAdmin()
        try directory.delete(id: id)
    }

    public func resetPassword(userID: String, newPassword: String) async throws {
        try requireAdmin()
    }

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

    /// Every project of the demo cluster (DemoProjects holds the fixtures).
    public static let projects: [Project] = DemoProjects.fixtures.map { DemoProjects.project($0) }
}
