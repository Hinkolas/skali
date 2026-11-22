import Foundation
import Synchronization

/// The demo cluster's object store: six volume servers at replication
/// 001, the projects' buckets and the platform's own. `roomy` runs the
/// 256 MB default with plenty of room; `full` is the cluster of issue #289,
/// 8 GB volumes on small disks with no slot left. The slots follow the
/// volume servers' own rule, so a saved volume size changes them as a real
/// cluster would.
public final class DemoObjectStorage: Sendable {
    public enum Scenario: String, Sendable {
        case roomy, full
    }

    private struct State {
        var scenario: Scenario
        var limitMb: Int?
        var updatedAt = Date.now.addingTimeInterval(-6 * 86_400)
    }

    private let state: Mutex<State>

    init(scenario: Scenario) {
        state = Mutex(State(scenario: scenario, limitMb: scenario == .full ? 8000 : nil))
    }

    private static let mib: Int64 = 1 << 20
    private static let gib: Int64 = 1 << 30
    private static let created = Date.now.addingTimeInterval(-120 * 86_400)
    private static let serverNodes = (2...7).map { "skali-app-0\($0)" }

    func store() -> ObjectStore {
        let state = state.withLock { $0 }
        let limit = state.limitMb ?? 256
        let buckets = Self.buckets(state.scenario)
        // Every bucket's volume copies, spread over the servers in turn.
        let replicas = buckets.reduce(0) { $0 + ($1.usage?.slots ?? 0) }
        let servers = Self.serverNodes.enumerated().map { index, node in
            let volumes = replicas / Self.serverNodes.count + (index < replicas % Self.serverNodes.count ? 1 : 0)
            let freeDisk = state.scenario == .full ? 16 * Self.gib : (34 - Int64(index) * 3) * Self.gib
            let writable = Array(repeating: 40 * Self.mib, count: volumes)
            let reserved = writable.reduce(Int64(0)) { $0 + max(0, Int64(limit) * Self.mib - $1) }
            let unclaimed = freeDisk - reserved
            let slots = max(1, volumes + (unclaimed > 0 ? Int(unclaimed / (Int64(limit) * Self.mib)) : 0))
            return ObjectStore.Server(
                url: "10.42.1.\(index + 3):8080", node: node, slots: slots, usedSlots: volumes,
                freeSlots: max(0, slots - volumes), freeDiskBytes: freeDisk, writableVolumeBytes: writable)
        }
        let free = servers.map(\.freeSlots)
        let headroom = volumeHeadroom(free: free, copies: 2)
        return ObjectStore(
            image: "chrislusf/seaweedfs:4.48", managed: true, masters: 3, volumeServers: servers.count,
            replication: "001",
            volumeSize: .init(limitMb: limit, default: state.limitMb == nil, defaultMb: 256, minMb: 64, maxMb: 30000),
            buckets: buckets.count,
            capacity: .init(
                slots: servers.reduce(0) { $0 + $1.slots }, usedSlots: replicas, freeSlots: free.reduce(0, +),
                volumeHeadroom: headroom, bucketHeadroom: headroom, volumesPerBucket: 1, copies: 2, servers: servers),
            observed: .init(
                state: "fresh", mastersReady: 3, volumeServersReady: servers.count, filerReady: true, s3Ready: true,
                volumeSizeLimitMb: limit),
            createdAt: Self.created, updatedAt: state.updatedAt)
    }

    func buckets() -> ObjectStoreBuckets {
        let scenario = state.withLock { $0.scenario }
        return ObjectStoreBuckets(
            buckets: Self.buckets(scenario),
            unclaimed: [
                .init(
                    collection: "b-uploads-3f9e21aa",
                    usage: .init(usedBytes: 1_200, diskBytes: 1_200, entryCount: 2, volumes: 1, slots: 2)),
            ],
            usageObserved: true)
    }

    func save(_ settings: ObjectStorageSettings) throws -> ObjectStore {
        if let limit = settings.volumeSizeLimitMb, !(64...30000).contains(limit) {
            throw APIError.server(
                status: 422, code: "bad_request", message: "the volume size limit must be between 64 and 30000 MB")
        }
        state.withLock {
            $0.limitMb = settings.volumeSizeLimitMb
            $0.updatedAt = .now
        }
        return store()
    }

    /// The projects' buckets and the platform's registry. Under 8 GB
    /// volumes every written bucket holds one volume, two copies.
    private static func buckets(_ scenario: Scenario) -> [ObjectStoreBuckets.Bucket] {
        let limit: Int64 = scenario == .full ? 8000 * mib : 256 * mib
        let fixtures: [(project: String, environment: String, key: String, used: Int64, quota: Int64)] = [
            ("eplan-b1", "production", "uploads", 6_900 * mib, 20 * gib),
            ("eplan-b2", "production", "uploads", 2_400 * mib, 20 * gib),
            ("himmelreich", "staging", "media", 1_150 * mib, 0),
            ("birefnet-proxy", "staging", "cutouts", 640 * mib, 5 * gib),
            ("nordlicht-api", "production", "exports", 212 * mib, 0),
            ("nordlicht-api", "staging", "exports", 38 * mib, 0),
            ("go-lexware-automation", "production", "invoices", 96 * mib, 2 * gib),
            ("himmelreich", "production", "media", 3_100 * mib, 0),
            ("maja", "preview", "assets", 4 * mib, 0),
            ("maja", "production", "assets", 410 * mib, 0),
            ("eplan-b1", "staging", "uploads", 0, 20 * gib),
            ("birefnet-proxy", "production", "cutouts", 1_900 * mib, 5 * gib),
        ]
        let services = fixtures.enumerated().map { index, fixture in
            let volumes = fixture.used == 0 ? 0 : Int((fixture.used + limit - 1) / limit)
            return ObjectStoreBuckets.Bucket(
                bucketName: "b-\(fixture.key)-\(String(format: "%08x", (index + 1) * 0x9e3779b1 & 0xffff_ffff))",
                owner: "service", serviceKey: fixture.key,
                project: .init(id: fixture.project, name: fixture.project),
                environment: .init(id: fixture.environment, name: fixture.environment),
                visibility: index == 4 ? "public-read" : "private", quotaBytes: fixture.quota,
                usage: .init(
                    usedBytes: fixture.used, diskBytes: fixture.used + fixture.used / 40, deletedBytes: fixture.used / 40,
                    entryCount: fixture.used / (180 * 1024), volumes: volumes, slots: volumes * 2),
                createdAt: created.addingTimeInterval(Double(index * 6) * 86_400))
        }
        let registryVolumes = Int((2_300 * mib + limit - 1) / limit)
        return services + [
            .init(
                bucketName: "skali-registry", owner: "system", systemKey: "platform/registry",
                usage: .init(
                    usedBytes: 2_300 * mib, diskBytes: 2_450 * mib, deletedBytes: 150 * mib, entryCount: 6_400,
                    volumes: registryVolumes, slots: registryVolumes * 2),
                createdAt: created),
        ]
    }
}
