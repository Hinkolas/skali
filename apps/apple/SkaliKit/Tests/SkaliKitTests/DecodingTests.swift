import Foundation
import Testing
@testable import SkaliKit

struct DecodingTests {
    @Test func decodesASummarizedProject() throws {
        let json = """
        {"projects":[{"id":"6f1c","name":"shop","display_name":"","source_mode":"managed",
          "created_at":"2026-09-01T10:00:00Z","updated_at":"2026-09-02T10:00:00.123456789Z",
          "access":{"role":"admin","member":true,"environments":{"production":"admin","secret":"none"}},
          "summary":{"environments":[
            {"id":"e1","name":"production","access":"admin","priority":"high","state":"active",
             "health":"healthy","health_evaluated_at":"2026-10-20T08:00:00.5Z"},
            {"id":"e2","name":"secret","access":"none"},
            {"id":"e3","name":"staging","access":"admin","health":"from-the-future"}],
           "service_counts":{"applications":2,"databases":1,"buckets":0},
           "updated_at":"2026-10-20T08:00:00Z"}}]}
        """
        let response = try JSONCoding.decoder.decode(ProjectsResponse.self, from: Data(json.utf8))
        let project = try #require(response.projects.first)
        #expect(project.label == "shop")
        #expect(project.serviceCount == 3)
        #expect(project.summary?.environments[1].health == nil)
        #expect(project.summary?.environments[2].health == .unknown)
        #expect(project.worstHealth == .unknown)
        #expect(project.environmentRollup.first?.name == "production")
    }

    @Test func decodesTheErrorEnvelope() throws {
        let json = #"{"error":{"code":"reauth_required","message":"confirm it is you"}}"#
        let envelope = try JSONCoding.decoder.decode(ErrorEnvelope.self, from: Data(json.utf8))
        #expect(envelope.error.code == "reauth_required")
    }
}
