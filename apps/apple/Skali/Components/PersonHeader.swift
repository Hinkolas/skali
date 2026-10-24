import SkaliKit
import SwiftUI

/// Avatar, name, email and the instance role, as the top of a person's
/// screen (your account, or a user an admin opened).
struct PersonHeader: View {
    let user: User
    var isYou = false

    var body: some View {
        VStack(spacing: 6) {
            Avatar(initials: user.initials, size: 88)
                .padding(.bottom, 6)
            Text(user.name.isEmpty ? user.email : user.name)
                .font(.title.weight(.bold))
                .multilineTextAlignment(.center)
            if !user.name.isEmpty {
                Text(user.email)
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
            }
            HStack(spacing: 6) {
                RoleChip(isAdmin: user.isAdmin)
                if isYou { RoleChip(text: "You", tint: .secondary) }
            }
            .padding(.top, 4)
        }
        .frame(maxWidth: .infinity)
        .accessibilityElement(children: .combine)
    }
}

/// "Instance Admin", "Member" or another short tag in a tinted capsule.
struct RoleChip: View {
    let text: String
    let tint: Color

    init(isAdmin: Bool) {
        text = isAdmin ? "Instance Admin" : "Member"
        tint = .accentColor
    }

    init(text: String, tint: Color) {
        self.text = text
        self.tint = tint
    }

    var body: some View {
        Text(text)
            .font(.caption.weight(.medium))
            .foregroundStyle(tint)
            .padding(.horizontal, 10)
            .padding(.vertical, 4)
            .background(tint.opacity(0.15), in: .capsule)
    }
}
