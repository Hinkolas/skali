import Foundation
import Security

/// Strong initial passwords for accounts an admin creates or resets, in
/// the shape Apple's strong passwords use: three groups of six, easy to
/// read aloud or type on another device ("xkVr2m-pq7hbn-Tw4zec").
public enum PasswordGenerator {
    // No 0/O, 1/l/I: the password is meant to be read and typed.
    private static let lower = Array("abcdefghijkmnopqrstuvwxyz")
    private static let upper = Array("ABCDEFGHJKLMNPQRSTUVWXYZ")
    private static let digits = Array("23456789")

    public static func generate() -> String {
        var characters = (0..<18).map { _ in lower[randomIndex(lower.count)] }
        // One uppercase letter and one digit, at random places.
        let upperAt = randomIndex(characters.count)
        var digitAt = randomIndex(characters.count)
        while digitAt == upperAt { digitAt = randomIndex(characters.count) }
        characters[upperAt] = upper[randomIndex(upper.count)]
        characters[digitAt] = digits[randomIndex(digits.count)]
        return stride(from: 0, to: 18, by: 6)
            .map { String(characters[$0..<$0 + 6]) }
            .joined(separator: "-")
    }

    /// A uniformly random index from the system's secure generator.
    private static func randomIndex(_ count: Int) -> Int {
        var generator = SecureRandom()
        return Int.random(in: 0..<count, using: &generator)
    }
}

private struct SecureRandom: RandomNumberGenerator {
    mutating func next() -> UInt64 {
        var value: UInt64 = 0
        let status = withUnsafeMutableBytes(of: &value) {
            SecRandomCopyBytes(kSecRandomDefault, $0.count, $0.baseAddress!)
        }
        precondition(status == errSecSuccess, "The system's random generator failed")
        return value
    }
}
