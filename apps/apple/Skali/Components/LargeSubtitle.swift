import SwiftUI

extension View {
    /// A short line under the large title, as Notes shows its count: body
    /// size in the iOS 26 large-subtitle slot, the same text under the
    /// small title once scrolled (unless `inline` is off, for a screen
    /// whose bar shows something else there), and room before the list
    /// starts. `monospaced` sets it like the Studio's code-ish subtitles
    /// ("rollouts · last deployed 19d ago").
    func largeSubtitle(_ text: String, dot: Color? = nil, inline: Bool = true, monospaced: Bool = false) -> some View {
        modifier(LargeSubtitleModifier(text: text, dot: dot, inline: inline, monospaced: monospaced))
    }
}

private struct LargeSubtitleModifier: ViewModifier {
    let text: String
    let dot: Color?
    let inline: Bool
    let monospaced: Bool

    func body(content: Content) -> some View {
        content
            .navigationSubtitle(inline ? text : "")
            .contentMargins(.top, 14, for: .scrollContent)
            .toolbar {
                ToolbarItem(placement: .largeSubtitle) {
                    HStack(spacing: 7) {
                        if let dot {
                            Circle().fill(dot).frame(width: 8, height: 8)
                                .accessibilityHidden(true)
                        }
                        Text(text)
                            .font(monospaced ? .callout.monospaced() : .body)
                            .foregroundStyle(.secondary)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.top, 4)
                }
            }
    }
}
