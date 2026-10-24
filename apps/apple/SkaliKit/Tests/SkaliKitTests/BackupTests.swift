import Foundation
import Testing
@testable import SkaliKit

struct BackupTests {
    @Test func decodesTheSummary() throws {
        let json = """
        {"target":{"name":"default","endpoint":"https://s3.example.com","region":"","bucket":"skali-backup","prefix":"",
          "access_key_id":"AK","created_at":"2026-09-01T10:00:00Z","updated_at":"2026-09-01T10:00:00Z"},
         "reachable":true,"checked_at":"2026-10-23T21:00:00.5+02:00",
         "snapshots":{"count":3,"bytes":450,"manual":2,"scheduled":1,"orphaned":1,
           "oldest_at":"2026-10-01T00:00:00Z","newest_at":"2026-10-23T00:00:00Z",
           "projects":[{"project":"demo","count":3,"bytes":450,"newest_at":"2026-10-23T00:00:00Z"}]},
         "runs":{"since":"2026-10-16T21:00:00Z","succeeded":4,"failed":1,"backing_up":0,"restoring":1,
           "last_succeeded":{"project":"demo","environment":"production","finished_at":"2026-10-22T03:00:00Z"},
           "last_failed":{"project":"demo","environment":"staging","finished_at":"2026-10-23T03:00:00Z","error":"dump failed"}},
         "schedules":{"environments":4,"scheduled":2}}
        """
        let summary = try JSONCoding.decoder.decode(BackupSummary.self, from: Data(json.utf8))
        #expect(summary.target.accessKeyId == "AK")
        #expect(summary.snapshots?.projects.first?.bytes == 450)
        #expect(summary.runs.lastFailed?.error == "dump failed")
        #expect(summary.failingNow)
        #expect(summary.schedules.scheduled == 2)
    }

    @Test func anUnreachableTargetHasNoSnapshots() throws {
        let json = """
        {"target":{"name":"default","endpoint":"https://s3.example.com","region":"","bucket":"b","prefix":"","access_key_id":"AK",
          "created_at":"2026-09-01T10:00:00Z","updated_at":"2026-09-01T10:00:00Z"},
         "reachable":false,"unreachable_reason":"bucket b does not exist","checked_at":"2026-10-23T21:00:00Z",
         "snapshots":null,
         "runs":{"since":"2026-10-16T21:00:00Z","succeeded":0,"failed":0,"backing_up":0,"restoring":0,"last_succeeded":null,"last_failed":null},
         "schedules":{"environments":0,"scheduled":0}}
        """
        let summary = try JSONCoding.decoder.decode(BackupSummary.self, from: Data(json.utf8))
        #expect(!summary.reachable)
        #expect(summary.snapshots == nil)
        #expect(!summary.failingNow)
    }

    @Test func anEditStartsWithoutTheSecret() {
        let target = BackupTarget(endpoint: "https://s3.example.com", bucket: "b", accessKeyId: "AK")
        var input = BackupTargetInput(target)
        #expect(input.secretAccessKey.isEmpty)
        #expect(!input.isComplete)
        input.secretAccessKey = "sk"
        #expect(input.isComplete)
    }

    @Test func theDemoTargetCanBeRemovedAndConfigured() async throws {
        let demo = DemoCluster(backups: .none)
        #expect(try await demo.backupSummary() == nil)
        _ = try await demo.saveBackupTarget(BackupTargetInput(
            endpoint: "https://s3.example.com", bucket: "b", accessKeyId: "AK", secretAccessKey: "sk"))
        #expect(try await demo.backupSummary()?.target.bucket == "b")
        try await demo.removeBackupTarget()
        #expect(try await demo.backupTarget() == nil)
    }
}
