import Foundation
import Synchronization

/// The demo cluster's backup target: configured with a few weeks of
/// snapshots, or not configured at all, and changeable in memory.
public final class DemoBackups: Sendable {
    public enum Scenario: String, Sendable {
        case configured, none
    }

    private let target: Mutex<BackupTarget?>

    init(scenario: Scenario) {
        target = Mutex(scenario == .configured ? Self.sample : nil)
    }

    private static let sample = BackupTarget(
        endpoint: "https://s3.eu-central-1.amazonaws.com", region: "eu-central-1", bucket: "skali-backup",
        prefix: "kilohertz", accessKeyId: "AKIAIOSFODNN7EXAMPLE",
        createdAt: .now.addingTimeInterval(-90 * 86_400), updatedAt: .now.addingTimeInterval(-30 * 86_400))

    func current() -> BackupTarget? {
        target.withLock { $0 }
    }

    func save(_ input: BackupTargetInput) throws -> BackupTarget {
        guard URL(string: input.endpoint)?.scheme?.hasPrefix("http") == true else {
            throw APIError.server(status: 422, code: "bad_request", message: "endpoint must be an http or https URL")
        }
        let saved = BackupTarget(
            endpoint: input.endpoint, region: input.region, bucket: input.bucket, prefix: input.prefix,
            accessKeyId: input.accessKeyId, createdAt: target.withLock { $0?.createdAt } ?? .now, updatedAt: .now)
        target.withLock { $0 = saved }
        return saved
    }

    func remove() throws {
        try target.withLock {
            guard $0 != nil else {
                throw APIError.server(status: 404, code: "not_found", message: "backup target not configured")
            }
            $0 = nil
        }
    }

    func summary() -> BackupSummary? {
        guard let target = current() else { return nil }
        let gib: Int64 = 1 << 30
        let hour: TimeInterval = 3600
        let projects: [BackupSummary.Snapshots.Project] = [
            .init(project: "eplan-b1", count: 21, bytes: 14 * gib + gib / 2, newestAt: .now.addingTimeInterval(-3 * hour)),
            .init(project: "himmelreich", count: 14, bytes: 9 * gib + gib / 5, newestAt: .now.addingTimeInterval(-2 * hour)),
            .init(project: "eplan-b2", count: 14, bytes: 6 * gib + gib / 3, newestAt: .now.addingTimeInterval(-3 * hour)),
            .init(project: "nordlicht-api", count: 9, bytes: 5 * gib + gib / 10, newestAt: .now.addingTimeInterval(-26 * hour)),
            .init(project: "go-lexware-automation", count: 6, bytes: 1 * gib + gib / 4, newestAt: .now.addingTimeInterval(-50 * hour)),
            .init(project: "birefnet-proxy", count: 3, bytes: gib / 3, newestAt: .now.addingTimeInterval(-9 * 24 * hour)),
        ]
        return BackupSummary(
            target: target, reachable: true, checkedAt: .now,
            snapshots: .init(
                count: projects.reduce(0) { $0 + $1.count }, bytes: projects.reduce(0) { $0 + $1.bytes },
                manual: 7, scheduled: 60, orphaned: 2,
                oldestAt: .now.addingTimeInterval(-62 * 24 * hour), newestAt: .now.addingTimeInterval(-2 * hour),
                projects: projects),
            runs: .init(
                since: .now.addingTimeInterval(-7 * 24 * hour), succeeded: 41, failed: 1, backingUp: 1, restoring: 0,
                lastSucceeded: .init(project: "himmelreich", environment: "production", finishedAt: .now.addingTimeInterval(-2 * hour)),
                lastFailed: .init(
                    project: "nordlicht-api", environment: "staging", finishedAt: .now.addingTimeInterval(-26 * hour),
                    error: "pg_dump: error: connection to server failed: FATAL: sorry, too many clients already")),
            schedules: .init(environments: 18, scheduled: 12))
    }
}
