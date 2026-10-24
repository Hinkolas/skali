import Foundation

/// The demo cluster's database pools: the shared pool every project uses,
/// and a dedicated one close to its connection limit.
enum DemoPools {
    private static let created = Date.now.addingTimeInterval(-120 * 86_400)
    private static let gib: Int64 = 1 << 30
    private static let mib: Int64 = 1 << 20

    static let pools: [DatabasePool] = [
        DatabasePool(
            name: "shared", class: "shared", major: 18, instances: 2, storageBytes: 20 * gib,
            image: "ghcr.io/cloudnative-pg/postgresql:18.1", nodePort: 31432,
            memory: .init(bytes: 2 * gib, auto: true, node: "skali-app-05"),
            observed: .init(phase: "Cluster in healthy state", instances: 2, readyInstances: 2, primary: "shared-1"),
            connections: .init(
                state: .available, checkedAt: .now.addingTimeInterval(-9), used: 23, limit: 97, maxConnections: 100),
            createdAt: created, updatedAt: created),
        DatabasePool(
            name: "nordlicht-api-production", class: "dedicated", major: 18, instances: 1,
            storageBytes: 50 * gib, image: "ghcr.io/cloudnative-pg/postgresql:18.1",
            memory: .init(bytes: 4 * gib, auto: false),
            observed: .init(phase: "Cluster in healthy state", instances: 1, readyInstances: 1, primary: "nordlicht-api-production-1"),
            connections: .init(
                state: .saturated, since: .now.addingTimeInterval(-25 * 60), checkedAt: .now.addingTimeInterval(-6),
                used: 91, limit: 97, maxConnections: 100),
            createdAt: created.addingTimeInterval(40 * 86_400), updatedAt: created),
    ]

    static func detail(_ name: String) -> DatabasePoolDetail? {
        guard let pool = pools.first(where: { $0.name == name }) else { return nil }
        let members: [DatabasePoolDetail.Member] = (1...pool.instances).map { index in
            .init(
                name: "\(pool.name)-\(index)", role: index == 1 ? "primary" : "replica",
                node: "skali-app-0\(4 + index)", ready: true, restarts: index == 1 ? 0 : 1,
                startedAt: .now.addingTimeInterval(-Double(9 + index) * 86_400), phase: "Running")
        }
        let databases: [DatabasePoolDetail.Database]
        let incidents: [DatabasePool.Incident]
        if pool.class == "shared" {
            let projects: [(String, String, Int64?, Int?)] = [
                ("birefnet-proxy", "staging", 184 * mib, 3), ("eplan-b1", "production", 1_310 * mib, 6),
                ("eplan-b2", "production", 920 * mib, 5), ("go-lexware-automation", "production", 42 * mib, 2),
                ("himmelreich", "staging", 268 * mib, 4), ("nordlicht-api", "staging", 77 * mib, 1),
                ("maja", "preview", nil, nil),
            ]
            databases = projects.map { project, environment, used, connections in
                .init(
                    databaseName: "\(project.replacingOccurrences(of: "-", with: "_"))_\(environment)_db",
                    roleName: "\(project.replacingOccurrences(of: "-", with: "_"))_\(environment)",
                    owner: "service", serviceKey: "db",
                    project: .init(id: project, name: project), environment: .init(id: environment, name: environment),
                    storageBytes: 5 * gib, usedBytes: used, connections: connections,
                    createdAt: created.addingTimeInterval(Double(project.count) * 86_400))
            } + [
                .init(
                    databaseName: "skali_registry", roleName: "skali_registry", owner: "system",
                    systemKey: "registry", storageBytes: 2 * gib, usedBytes: 36 * mib, connections: 2,
                    createdAt: created),
            ]
            incidents = [
                .init(
                    id: "8d1f6a52-3c0e-4a8e-9a43-2b7c9f0e1d11", code: "connections-exhausted",
                    message: "FATAL: sorry, too many clients already",
                    openedAt: .now.addingTimeInterval(-9 * 86_400 - 3600),
                    closedAt: .now.addingTimeInterval(-9 * 86_400 - 2700)),
            ]
        } else {
            databases = [
                .init(
                    databaseName: "nordlicht_api_production_db", roleName: "nordlicht_api_production",
                    owner: "service", serviceKey: "db",
                    project: .init(id: "nordlicht-api", name: "nordlicht-api"),
                    environment: .init(id: "production", name: "production"),
                    storageBytes: 50 * gib, usedBytes: 17 * gib, connections: 91, createdAt: pool.createdAt),
            ]
            incidents = []
        }
        return DatabasePoolDetail(pool: pool, members: members, databases: databases, incidents: incidents)
    }

    static func metrics(_ name: String, window: NodeMetrics.Window) -> DatabasePoolMetrics {
        let busy = name != "shared"
        let (count, step): (Int, Int) = switch window {
        case .hour: (60, 60)
        case .day: (96, 900)
        case .week: (168, 3600)
        }
        let end = Date.now
        let timestamps = (0..<count).map { end.addingTimeInterval(-Double((count - 1 - $0) * step)) }
        let load: [Double] = timestamps.map { time in
            let seconds = time.timeIntervalSince1970
            let day = (sin(seconds / 86_400 * 2 * .pi) + 1) / 2
            let swing = sin(seconds / 21_600 * 2 * .pi + (busy ? 1 : 0)) * 0.05
            return min(1, max(0, (busy ? 0.75 : 0.25) + day * 0.15 + swing))
        }
        let budget = busy ? 4 * gib : 2 * gib
        let size: Int64 = busy ? 17 * gib : 2_840 * mib
        let perStep = Double(step) / 60
        let cpu = load.map { Int?(Int($0 * (busy ? 1_400 : 600))) }
        let memory = load.map { Int64?(Int64((0.45 + $0 * 0.35) * Double(budget))) }
        let connections = load.map { Int?(Int($0 * (busy ? 96 : 40))) }
        let commits = load.map { Int?(Int($0 * (busy ? 900 : 260) * perStep)) }
        let rollbacks = load.map { Int?(Int($0 * (busy ? 12 : 3) * perStep)) }
        let ratio = load.map { Double?(0.985 - $0 * 0.02) }
        let bytes = timestamps.indices.map { Int64?(size - Int64(count - $0) * (busy ? 4 : 1) * mib) }
        return DatabasePoolMetrics(
            pool: name, window: window, stepSeconds: step, timestamps: timestamps,
            cpuMillicores: cpu, memoryBytes: memory, connections: connections, commits: commits,
            rollbacks: rollbacks, blksHit: [], blksRead: [], cacheHitRatio: ratio, databaseBytes: bytes,
            instancesReady: timestamps.map { _ in busy ? 1 : 2 },
            current: .init(
                sampledAt: end, cpuMillicores: cpu.last.flatMap(\.self) ?? 0,
                memoryBytes: memory.last.flatMap(\.self) ?? 0, instances: busy ? 1 : 2,
                instancesReady: busy ? 1 : 2, connections: busy ? 91 : 23,
                databaseBytes: bytes.last.flatMap(\.self), memoryBudgetBytes: budget, maxConnections: 100))
    }
}
