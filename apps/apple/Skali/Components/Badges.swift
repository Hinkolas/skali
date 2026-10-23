import SkaliKit
import SwiftUI
import UIKit

/// A service kind's icon on its tinted tile, with "×2" when there are more:
/// the Studio's kind chip (TypeBadge). The tile is a square without a count,
/// and the count is set like the listing's text beside it.
struct ServiceKindBadge: View {
    let kind: ServiceKind
    let count: Int

    @ScaledMetric(relativeTo: .caption) private var height: CGFloat = 22

    var body: some View {
        HStack(spacing: 3) {
            Image(systemName: kind.systemImage)
                .font(.system(
                    size: SymbolSizing.pointSize(for: kind.systemImage, base: height * 0.58, target: height * 0.64),
                    weight: .medium))
            if count > 1 {
                Text("×\(count)")
                    .font(.caption.monospaced().weight(.medium))
            }
        }
        .foregroundStyle(kind.tint)
        .padding(.horizontal, count > 1 ? height * 0.28 : 0)
        .frame(minWidth: height, minHeight: height)
        .background(kind.tint.opacity(0.16), in: .rect(cornerRadius: height * 0.28))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(count) \(count == 1 ? kind.label.lowercased() : kind.pluralLabel)")
    }
}

/// A small capsule for environment names and other tags: neutral, or in
/// a color for a state that wants attention ("not backed up").
struct TagChip: View {
    let text: String
    var tint: Color?

    var body: some View {
        Text(text)
            .font(.caption2.monospaced())
            .foregroundStyle(tint.map(AnyShapeStyle.init) ?? AnyShapeStyle(.secondary))
            .lineLimit(1)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(tint.map { AnyShapeStyle($0.opacity(0.15)) } ?? AnyShapeStyle(.fill.tertiary),
                        in: .rect(cornerRadius: 5))
            .fixedSize()
    }
}

/// The health dot with a soft halo, as in the Studio's listings.
struct HealthDot: View {
    let health: ServiceHealth

    var body: some View {
        Circle()
            .fill(health.tint)
            .frame(width: 8, height: 8)
            .padding(2.5)
            .background(health.tint.opacity(0.2), in: .circle)
            .accessibilityElement()
            .accessibilityLabel(health.label)
    }
}

/// The colored tile behind a Settings row's symbol. Tile and glyph scale
/// with Dynamic Type; `SymbolSizing` keeps wide and narrow glyphs alike.
struct SettingsIcon: View {
    let systemImage: String
    var tint: Color = .secondary

    @ScaledMetric(relativeTo: .body) private var side: CGFloat = 30

    var body: some View {
        Image(systemName: systemImage)
            .font(.system(
                size: SymbolSizing.pointSize(for: systemImage, base: side * 0.47, target: side * 0.57),
                weight: .medium))
            .foregroundStyle(tint)
            .frame(width: side, height: side)
            .background(tint.opacity(0.15), in: .rect(cornerRadius: side * 0.26))
            .accessibilityHidden(true)
    }
}

/// SF Symbols differ in shape at one point size (person.2 is far wider than
/// cylinder), so tiles that show several of them side by side read uneven.
/// The point size is reduced until the glyph's area matches a `target`
/// square. Changing the point size, rather than scaling the image, keeps
/// the stroke weight equal across tiles.
enum SymbolSizing {
    static func pointSize(for systemImage: String, base: CGFloat, target: CGFloat) -> CGFloat {
        let configuration = UIImage.SymbolConfiguration(pointSize: base, weight: .medium)
        guard let size = UIImage(systemName: systemImage, withConfiguration: configuration)?.size,
              size.width > 0, size.height > 0
        else { return base }
        return base * min(1, target / (size.width * size.height).squareRoot())
    }
}

/// The initials avatar for an account.
struct Avatar: View {
    let initials: String
    var size: CGFloat = 56

    var body: some View {
        Text(initials)
            .font(.system(size: size * 0.36, weight: .semibold, design: .rounded))
            .foregroundStyle(.tint)
            .frame(width: size, height: size)
            .background(Color.accentColor.opacity(0.18), in: .circle)
            .overlay(Circle().strokeBorder(Color.accentColor.opacity(0.35)))
            .accessibilityHidden(true)
    }
}

extension String {
    /// The text with break opportunities after `_`, `/`, `:`, `@`, `.`
    /// and `-`, so identifiers and URLs wrap at their seams instead of
    /// being hyphenated mid-word. For display only; copy the original.
    var wrappingAtSeams: String {
        var out = ""
        for character in self {
            out.append(character)
            if "_/:@.-".contains(character) { out.append("\u{200B}") }
        }
        return out
    }
}

extension View {
    /// Typesets text as code rather than language: identifiers, URLs and
    /// log lines must not get hyphens where they wrap. "zxx" is the code
    /// for no linguistic content, which carries no hyphenation rules.
    func typesetAsCode() -> some View {
        typesettingLanguage(Locale.Language(identifier: "zxx"))
    }
}
