import Foundation
import Testing
@testable import SkaliKit

struct UserTests {
    private let user = User(
        id: "u", email: "grace@example.com", name: "Grace Hopper", role: "member",
        createProjects: false, twoFactorEnabled: false, createdAt: .now)

    @Test func patchCarriesOnlyWhatChanged() {
        #expect(UserPatch(from: user, name: " Grace Hopper ", role: "member", createProjects: false, mayChangeRole: true).isEmpty)
        let renamed = UserPatch(from: user, name: "Grace B. Hopper", role: "member", createProjects: true, mayChangeRole: true)
        #expect(renamed.name == "Grace B. Hopper")
        #expect(renamed.role == nil)
        #expect(renamed.createProjects == true)
        // Your own role is locked; admins always create projects.
        let promoted = UserPatch(from: user, name: user.name, role: "admin", createProjects: true, mayChangeRole: false)
        #expect(promoted.isEmpty)
    }

    @Test func generatedPasswordsAreStrongAndReadable() {
        for _ in 0..<50 {
            let password = PasswordGenerator.generate()
            #expect(password.count == 20)
            let groups = password.components(separatedBy: "-").map(\.count)
            #expect(groups == [6, 6, 6])
            #expect(password.contains { $0.isUppercase })
            #expect(password.contains { $0.isNumber })
            #expect(!password.contains(where: { "0O1lI".contains($0) }))
        }
        #expect(PasswordGenerator.generate() != PasswordGenerator.generate())
    }

    @Test func theDemoDirectoryRefusesWhatTheDaemonRefuses() async throws {
        let demo = DemoCluster()
        await #expect(throws: APIError.self) { try await demo.deleteUser(id: DemoCluster.user.id) }
        await #expect(throws: APIError.self) {
            try await demo.updateUser(id: DemoCluster.user.id, UserPatch(role: "member"))
        }
        let created = try await demo.createUser(
            NewUser(email: "New@Example.com", name: "New", password: "secret-password", role: "member", createProjects: false))
        #expect(created.email == "new@example.com")
        await #expect(throws: APIError.self) {
            try await demo.createUser(NewUser(email: "new@example.com", name: "", password: "x", role: "member", createProjects: false))
        }
        try await demo.deleteUser(id: created.id)
        let remaining = try await demo.users()
        #expect(!remaining.contains { $0.id == created.id })
    }
}
