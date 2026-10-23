import Foundation
import Synchronization

/// The demo cluster's user directory, changeable in memory and refusing
/// what the daemon refuses: your own role, deleting yourself or the last
/// admin, and an email that is taken.
final class DemoUsers: Sendable {
    private let users: Mutex<[User]>
    /// The signed-in account, whose own role and deletion are refused.
    private let selfID: String

    init(signedIn: User) {
        selfID = signedIn.id
        let created = Date.now.addingTimeInterval(-200 * 86_400)
        func user(_ index: Int, _ name: String, _ email: String, admin: Bool = false, projects: Bool = false, twoFactor: Bool = false) -> User {
            User(
                id: "00000000-0000-0000-0000-0000000000\(String(format: "%02d", index))", email: email, name: name,
                role: admin ? "admin" : "member", createProjects: admin || projects,
                twoFactorEnabled: twoFactor, createdAt: created.addingTimeInterval(Double(index) * 9 * 86_400))
        }
        users = Mutex([
            DemoCluster.user,
            DemoCluster.member,
            user(3, "Alan Turing", "alan@example.com", admin: true, twoFactor: true),
            user(4, "Katherine Johnson", "katherine@example.com", projects: true, twoFactor: true),
            user(5, "Margaret Hamilton", "margaret@example.com", projects: true),
            user(6, "Edsger Dijkstra", "edsger@example.com"),
            user(7, "", "ci-bot@example.com"),
        ])
    }

    func list() -> [User] {
        users.withLock { $0 }
    }

    func create(_ new: NewUser) throws -> User {
        try users.withLock { users in
            let email = new.email.trimmingCharacters(in: .whitespaces).lowercased()
            guard !users.contains(where: { $0.email == email }) else {
                throw APIError.server(status: 409, code: "conflict", message: "a user with this email already exists")
            }
            let user = User(
                id: UUID().uuidString.lowercased(), email: email, name: new.name, role: new.role,
                createProjects: new.role == "admin" || new.createProjects, twoFactorEnabled: false, createdAt: .now)
            users.append(user)
            return user
        }
    }

    func update(id: String, _ patch: UserPatch) throws -> User {
        try users.withLock { users in
            guard let index = users.firstIndex(where: { $0.id == id }) else {
                throw APIError.server(status: 404, code: "not_found", message: "user not found")
            }
            let old = users[index]
            if let role = patch.role, role != old.role {
                if id == selfID {
                    throw APIError.server(status: 409, code: "conflict", message: "you cannot change your own role")
                }
                if old.role == "admin" && users.count(where: { $0.role == "admin" }) == 1 {
                    throw APIError.server(status: 409, code: "conflict", message: "cannot demote the last admin")
                }
            }
            let role = patch.role ?? old.role
            let user = User(
                id: old.id, email: old.email, name: patch.name ?? old.name, role: role,
                createProjects: role == "admin" || (patch.createProjects ?? old.createProjects),
                twoFactorEnabled: old.twoFactorEnabled, createdAt: old.createdAt)
            users[index] = user
            return user
        }
    }

    func delete(id: String) throws {
        try users.withLock { users in
            if id == selfID {
                throw APIError.server(status: 409, code: "conflict", message: "you cannot delete your own account")
            }
            if users.first(where: { $0.id == id })?.role == "admin" && users.count(where: { $0.role == "admin" }) == 1 {
                throw APIError.server(status: 409, code: "conflict", message: "cannot delete the last admin")
            }
            users.removeAll { $0.id == id }
        }
    }
}
