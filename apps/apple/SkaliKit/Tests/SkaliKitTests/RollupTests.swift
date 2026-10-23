import Foundation
import Testing
@testable import SkaliKit

struct RollupTests {
    private func project(_ name: String, health: [ServiceHealth], activity: TimeInterval) -> Project {
        Project(
            id: name, name: name, createdAt: .distantPast, updatedAt: .distantPast,
            access: ProjectAccess(role: "viewer", member: true),
            summary: ProjectSummary(
                environments: health.enumerated().map { SummaryEnvironment(id: "\($0.offset)", name: "env\($0.offset)", health: $0.element) },
                serviceCounts: ServiceCounts(applications: 1, databases: 0, buckets: 0),
                updatedAt: Date(timeIntervalSince1970: activity)))
    }

    @Test func worstHealthWins() {
        #expect(project("a", health: [.healthy, .degraded, .progressing], activity: 0).worstHealth == .degraded)
        #expect(project("a", health: [.healthy], activity: 0).worstHealth == .healthy)
        #expect(project("a", health: [], activity: 0).worstHealth == .unknown)
    }

    @Test func sortsByNameNaturallyAndByActivity() {
        let projects = [
            project("web-10", health: [], activity: 3),
            project("web-2", health: [], activity: 1),
            project("Alpha", health: [], activity: 2),
        ]
        #expect(ProjectSort.nameAscending.sorted(projects).map(\.name) == ["Alpha", "web-2", "web-10"])
        #expect(ProjectSort.updatedNewest.sorted(projects).map(\.name) == ["web-10", "Alpha", "web-2"])
    }

    @Test func compactAgeSteps() {
        let now = Date(timeIntervalSince1970: 1_000_000)
        #expect(compactAge(since: now.addingTimeInterval(-42), now: now) == "42s")
        #expect(compactAge(since: now.addingTimeInterval(-7 * 60), now: now) == "7m")
        #expect(compactAge(since: now.addingTimeInterval(-5 * 3600), now: now) == "5h")
        #expect(compactAge(since: now.addingTimeInterval(-42 * 86_400), now: now) == "42d")
    }

    @Test func initials() {
        #expect(DemoCluster.user.initials == "AL")
    }
}
