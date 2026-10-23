import Foundation
import Testing
@testable import SkaliKit

struct PoolTests {
    private let detailJSON = """
    {"name":"shared","class":"shared","engine":"postgres","major":18,"instances":1,"storage_bytes":21474836480,
     "image":"ghcr.io/cloudnative-pg/postgresql:18.1","node_port":null,"state":"ready",
     "memory":{"bytes":2147483648,"auto":true,"node":"skali-app-05","capped":false},
     "parameters":{"effective":{"max_connections":"100"},"overrides":{},"restart_keys":[],"allowed":[]},
     "observed":{"phase":"Cluster in healthy state","instances":1,"ready_instances":1,"primary":"shared-1"},
     "connections":{"state":"refusing","reason":"too many clients","since":"2026-10-23T10:00:00Z",
       "checked_at":"2026-10-23T10:05:00Z","used":97,"limit":97,"max_connections":100,
       "incident":{"id":"i1","code":"connections-exhausted","message":"FATAL","opened_at":"2026-10-23T10:00:30Z","closed_at":null}},
     "created_at":"2026-06-01T00:00:00Z","updated_at":"2026-06-01T00:00:00Z",
     "members":[{"name":"shared-1","role":"primary","node":"skali-app-05","ready":true,"restarts":0,"started_at":null,"phase":"Running"},
                {"name":"shared-2","role":"","ready":false,"restarts":2,"started_at":null,"phase":"Pending"}],
     "databases":[{"database_name":"skali_registry","role_name":"skali_registry","owner":"system","system_key":"registry",
                   "project":null,"environment":null,"phase":"ready","storage_bytes":1,"used_bytes":null,"connections":null,
                   "created_at":"2026-06-01T00:00:00Z"},
                  {"database_name":"shop_db","role_name":"shop","owner":"service","service_key":"db",
                   "project":{"id":"p","name":"shop","display_name":"Shop"},"environment":{"id":"e","name":"production"},
                   "phase":"ready","storage_bytes":1,"used_bytes":42,"connections":3,"created_at":"2026-06-01T00:00:00Z"}],
     "incidents":[]}
    """

    @Test func decodesThePoolDetail() throws {
        let detail = try JSONCoding.decoder.decode(DatabasePoolDetail.self, from: Data(detailJSON.utf8))
        #expect(detail.pool.name == "shared")
        #expect(detail.pool.status == .refusing)
        #expect(detail.pool.connectionFraction == 1)
        #expect(detail.pool.connections?.incident?.closedAt == nil)
        #expect(detail.memberSummary == "1 primary · 1 starting")
        #expect(detail.databasesBySize.map(\.databaseName) == ["shop_db", "skali_registry"])
        #expect(detail.databases[0].ownerLabel == "system · registry")
        #expect(detail.databases[1].ownerLabel == "Shop · production · db")
        #expect(detail.pool.budgetText == "2 GiB · automatic from skali-app-05")
    }

    @Test func statusReadsConnectionsBeforeInstances() {
        func pool(_ connections: DatabasePool.Connections.State?, phase: String? = "Cluster in healthy state", ready: Int = 1) -> DatabasePool {
            DatabasePool(
                name: "p", class: "shared", major: 18, instances: 1, storageBytes: 1,
                memory: .init(bytes: nil, auto: true),
                observed: phase.map { .init(phase: $0, instances: 1, readyInstances: ready) },
                connections: connections.map { .init(state: $0) }, createdAt: .now, updatedAt: .now)
        }
        #expect(pool(.available).status == .healthy)
        #expect(pool(.saturated).status == .saturated)
        #expect(pool(.available, ready: 0).status == .degraded(phase: "Cluster in healthy state"))
        #expect(pool(.available, phase: nil).status == .notObserved)
        #expect(pool(.unknown).status == .connectionsUnknown)
        #expect(pool(nil).status == .healthy)
        #expect(pool(nil).budgetText == "unknown · no database node observed yet")
    }

    @Test func transactionsAddCommitsAndRollbacks() async throws {
        let metrics = try await DemoCluster().databasePoolMetrics(name: "shared", window: .hour)
        #expect(metrics.hasSamples)
        #expect(metrics.transactions.count == metrics.timestamps.count)
        #expect(metrics.current?.maxConnections == 100)
    }
}
