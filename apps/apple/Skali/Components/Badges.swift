import SkaliKit
import SwiftUI
import UIKit

/// A service kind's icon on its tinted tile, with "×2" when there are more.
struct ServiceKindBadge: View {
    let kind: ServiceKind
    let count: Int

    var body: some View {
        HStack(spacing: 3) {
            Image(systemName: kind.systemImage)
                .imageScale(.small)
            if count > 1 {
                Text("×\(count)")
                    .monospacedDigit()
            }
        }
        .font(.footnote.weight(.medium))
        .foregroundStyle(kind.tint)
        .padding(.horizontal, 7)
        .frame(minWidth: 26, minHeight: 24)
        .background(kind.tint.opacity(0.16), in: .rect(cornerRadius: 7))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(count) \(count == 1 ? kind.label.lowercased() : kind.pluralLabel)")
    }
}

/// A neutral capsule for environment names and other tags.
struct TagChip: View {
    let text: String

    var body: some View {
        Text(text)
            .font(.caption.monospaced())
            .foregroundStyle(.secondary)
            .lineLimit(1)
            .padding(.horizontal, 7)
            .padding(.vertical, 2)
            .background(.fill.tertiary, in: .rect(cornerRadius: 6))
            .fixedSize()
    }
}

/// The health dot with a soft halo, as in the Studio's listings.
struct HealthDot: View {
    let health: ServiceHealth

    var body: some View {
        Circle()
            .fill(health.tint)
            .frame(width: 10, height: 10)
            .padding(3)
            .background(health.tint.opacity(0.2), in: .circle)
            .accessibilityElement()
            .accessibilityLabel(health.label)
    }
}

/// The colored tile behind a Settings row's symbol. SF Symbols differ in
/// shape at one point size (person.2 is far wider than cylinder), so each
/// glyph's point size is reduced until its area matches a square glyph's.
/// Changing the point size, not scaling the image, keeps the stroke weight
/// the same on every tile. Tile and glyph scale with Dynamic Type.
struct SettingsIcon: View {
    let systemImage: String
    var tint: Color = .secondary

    @ScaledMetric(relativeTo: .body) private var side: CGFloat = 30

    var body: some View {
        Image(systemName: systemImage)
            .font(.system(size: pointSize, weight: .medium))
            .foregroundStyle(tint)
            .frame(width: side, height: side)
            .background(tint.opacity(0.15), in: .rect(cornerRadius: side * 0.26))
            .accessibilityHidden(true)
    }

    private var pointSize: CGFloat {
        let base = side * 0.47
        let target = side * 0.57
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
