import SwiftUI

extension View {
    /// A short line under the large title, as Notes shows its count: body
    /// size in the iOS 26 large-subtitle slot, the same text under the
    /// small title once scrolled, and room before the list starts.
    func largeSubtitle(_ text: String, dot: Color? = nil) -> some View {
        modifier(LargeSubtitleModifier(text: text, dot: dot))
    }
}

private struct LargeSubtitleModifier: ViewModifier {
    let text: String
    let dot: Color?

    func body(content: Content) -> some View {
        content
            .navigationSubtitle(text)
            .contentMargins(.top, 14, for: .scrollContent)
            .toolbar {
                ToolbarItem(placement: .largeSubtitle) {
                    HStack(spacing: 7) {
                        if let dot {
                            Circle().fill(dot).frame(width: 8, height: 8)
                                .accessibilityHidden(true)
                        }
                        Text(text)
                            .font(.body)
                            .foregroundStyle(.secondary)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.top, 4)
                }
            }
    }
}
