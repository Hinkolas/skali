import Foundation
import Testing
@testable import SkaliKit

struct ObjectStorageTests {
    private let storeJSON = """
    {"store":{"name":"seaweed","state":"active","image":"chrislusf/seaweedfs:4.48","managed":true,"masters":3,
     "volume_servers":2,"replication":"001",
     "volume_size":{"limit_mb":256,"default":true,"default_mb":256,"min_mb":64,"max_mb":30000},
     "buckets":6,
     "capacity":{"slots":40,"used_slots":12,"free_slots":28,"volume_headroom":14,"bucket_headroom":14,
       "volumes_per_bucket":1,"copies":2,
       "servers":[{"url":"10.0.0.1:8080","node":"node-a","slots":20,"used_slots":6,"free_slots":14,
                   "free_disk_bytes":null,"writable_volume_bytes":[]},
                  {"url":"10.0.0.2:8080","slots":20,"used_slots":6,"free_slots":14,
                   "free_disk_bytes":null,"writable_volume_bytes":null}]},
     "observed":{"state":"fresh","stale_since":null,"masters_ready":3,"volume_servers_ready":2,"filer_ready":true,
       "s3_ready":true,"under_replicated_volumes":0,"replication_pending_volumes":0,"volume_size_limit_mb":256},
     "created_at":"2026-10-01T10:00:00Z","updated_at":"2026-11-20T10:00:00Z"}}
    """

    private let bucketsJSON = """
    {"buckets":[{"bucket_name":"skali-registry","owner":"system","system_key":"platform/registry","project":null,
       "environment":null,"phase":"provisioned","visibility":"private","quota_bytes":0,
       "usage":{"used_bytes":10,"disk_bytes":10,"deleted_bytes":0,"entry_count":1,"volumes":1,"slots":2},
       "created_at":"2026-10-01T10:00:00Z"},
      {"bucket_name":"b-uploads-1","owner":"service","service_key":"uploads",
       "project":{"id":"p","name":"shop","display_name":"Shop"},"environment":{"id":"e","name":"production"},
       "phase":"provisioned","visibility":"public-read","quota_bytes":1073741824,
       "usage":{"used_bytes":800,"disk_bytes":900,"deleted_bytes":100,"entry_count":12,"volumes":1,"slots":2},
       "created_at":"2026-10-02T10:00:00Z"},
      {"bucket_name":"b-new-1","owner":"service","service_key":"new","project":{"id":"p","name":"shop","display_name":""},
       "environment":{"id":"e","name":"staging"},"phase":"bound","visibility":"private","quota_bytes":0,"usage":null,
       "created_at":"2026-10-03T10:00:00Z"}],
     "unclaimed":[{"collection":"b-gone","usage":{"used_bytes":5,"disk_bytes":5,"deleted_bytes":0,"entry_count":1,"volumes":1,"slots":2}}],
     "usage_observed":true}
    """

    private func store() throws -> ObjectStore {
        try JSONCoding.decoder.decode(ObjectStoreResponse.self, from: Data(storeJSON.utf8)).store
    }

    @Test func decodesTheStore() throws {
        let store = try store()
        #expect(store.volumeSize.limitMb == 256)
        #expect(store.volumeSize.default)
        #expect(store.capacity?.servers.first?.node == "node-a")
        #expect(store.capacity?.servers.last?.writableVolumeBytes == nil)
        #expect(store.observed?.s3Ready == true)
        #expect(store.engineVersion == "4.48")
        #expect(store.slotsPerBucket == 2)
        #expect(store.headroom == .ok)
        #expect(!store.isDegraded)
    }

    @Test func decodesTheBuckets() throws {
        let inventory = try JSONCoding.decoder.decode(ObjectStoreBuckets.self, from: Data(bucketsJSON.utf8))
        #expect(inventory.bucketsBySize.map(\.bucketName) == ["b-uploads-1", "skali-registry", "b-new-1"])
        #expect(inventory.buckets[0].ownerLabel == "system · platform/registry")
        #expect(inventory.buckets[1].ownerLabel == "Shop · production · uploads")
        #expect(inventory.buckets[2].ownerLabel == "shop · staging · new")
        #expect(inventory.storedBytes == 815)
        #expect(inventory.awaitingVacuumBytes == 100)
        #expect(inventory.unclaimed.first?.usage.slots == 2)
    }

    @Test func headroomFollowsTheBucketsLeft() throws {
        let base = try store()
        func with(buckets: Int? = nil, headroom: Int, volumes: Int = 14) -> ObjectStore {
            ObjectStore(
                image: base.image, managed: true, masters: 3, volumeServers: 2, replication: "001",
                volumeSize: base.volumeSize, buckets: buckets ?? base.buckets,
                capacity: .init(
                    slots: 40, usedSlots: 12, freeSlots: 28, volumeHeadroom: volumes, bucketHeadroom: headroom,
                    volumesPerBucket: 1, copies: 2, servers: []),
                observed: nil, createdAt: .now, updatedAt: .now)
        }
        #expect(with(headroom: 0).headroom == .exhausted)
        #expect(with(headroom: 10, volumes: 0).headroom == .exhausted)
        #expect(with(headroom: 4).headroom == .low)
        #expect(with(buckets: 200, headroom: 20).headroom == .low)
        #expect(with(buckets: 100, headroom: 20).headroom == .ok)
    }

    @Test func volumeHeadroomNeedsADifferentServerPerCopy() {
        #expect(volumeHeadroom(free: [5, 3], copies: 1) == 8)
        #expect(volumeHeadroom(free: [100, 3], copies: 2) == 3)
        #expect(volumeHeadroom(free: [4, 4, 4], copies: 2) == 6)
        #expect(volumeHeadroom(free: [10, 0], copies: 2) == 0)
        #expect(volumeHeadroom(free: [], copies: 1) == 0)
    }

    /// The cluster of issue #289: four nearly empty volumes per server
    /// reserve 32 GB against 16 GB free at 8 GB; at 256 MB they reserve
    /// about 1 GB, leaving (16 GB - 1020 MB) / 256 MB = 60 slots each.
    @Test func estimatesFollowTheVolumeServersRule() {
        let mb: Int64 = 1 << 20
        let servers = (1...6).map { index in
            ObjectStore.Server(
                url: "10.0.0.\(index):8080", slots: 4, usedSlots: 4, freeSlots: 0,
                freeDiskBytes: 16 * 1024 * mb, writableVolumeBytes: [mb, mb, mb, mb])
        }
        let store = ObjectStore(
            image: "chrislusf/seaweedfs:4.48", managed: true, masters: 3, volumeServers: 6, replication: "001",
            volumeSize: .init(limitMb: 8000, default: false, defaultMb: 256, minMb: 64, maxMb: 30000), buckets: 12,
            capacity: .init(
                slots: 24, usedSlots: 24, freeSlots: 0, volumeHeadroom: 0, bucketHeadroom: 0,
                volumesPerBucket: 1, copies: 2, servers: servers),
            observed: nil, createdAt: .now, updatedAt: .now)
        #expect(store.headroom == .exhausted)
        #expect(store.estimate(volumeSizeMb: 256) == ObjectStoreEstimate(freeSlots: 360, bucketHeadroom: 180, rough: false))
        #expect(store.estimate(volumeSizeMb: 8000) == ObjectStoreEstimate(freeSlots: 0, bucketHeadroom: 0, rough: false))
    }

    @Test func estimatesRecoverOrScaleWithoutFreeDisk() throws {
        let store = try store()
        // node-a: no writable volume and 14 free at 256 MB is 14 * 256 MB of
        // disk, 28 slots at 128 MB. The other server's volumes are unread,
        // so its 14 free slots are scaled, and the estimate is rough.
        #expect(store.estimate(volumeSizeMb: 128) == ObjectStoreEstimate(freeSlots: 56, bucketHeadroom: 28, rough: true))
        #expect(store.estimate(volumeSizeMb: 256) == ObjectStoreEstimate(freeSlots: 28, bucketHeadroom: 14, rough: false))
    }

    @Test func settingsSendAnExplicitNullForTheDefault() throws {
        let reset = try JSONCoding.encoder.encode(ObjectStorageSettings(volumeSizeLimitMb: nil))
        #expect(String(decoding: reset, as: UTF8.self) == #"{"volume_size_limit_mb":null}"#)
        let set = try JSONCoding.encoder.encode(ObjectStorageSettings(volumeSizeLimitMb: 512))
        #expect(String(decoding: set, as: UTF8.self) == #"{"volume_size_limit_mb":512}"#)
    }

    @Test func volumeSizesParseAndPrint() {
        #expect(parseVolumeSize("256") == 256)
        #expect(parseVolumeSize(" 512 MB ") == 512)
        #expect(parseVolumeSize("1.5gb") == 1500)
        #expect(parseVolumeSize("1.5") == nil)
        #expect(parseVolumeSize("big") == nil)
        #expect(parseVolumeSize("0") == nil)
        #expect(formatVolumeSize(256) == "256 MB")
        #expect(formatVolumeSize(8000) == "8 GB")
        #expect(formatVolumeSize(1500) == "1.5 GB")
    }

    @Test func theFullDemoStoreMakesRoomUnderASmallerSize() async throws {
        let demo = DemoCluster(objectStorage: .full)
        let full = try #require(try await demo.objectStore())
        #expect(full.headroom == .exhausted)
        let estimate = try #require(full.estimate(volumeSizeMb: 256))
        let saved = try await demo.saveObjectStorageSettings(.init(volumeSizeLimitMb: nil))
        #expect(saved.volumeSize.default)
        #expect(saved.headroom == .ok)
        #expect(saved.capacity?.bucketHeadroom == estimate.bucketHeadroom)
        await #expect(throws: APIError.self) {
            try await demo.saveObjectStorageSettings(.init(volumeSizeLimitMb: 10))
        }
    }
}
