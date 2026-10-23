import SkaliKit
import SwiftUI

extension Color {
    /// The usage colors the node screens share: CPU in the accent's
    /// lavender, memory in blue, disk in amber, as the Studio's bars.
    static let usageCPU = Color.serviceApplication
    static let usageMemory = Color.cyan
    static let usageDisk = Color.orange
}

/// The server glyph on its tile, green while the node is ready.
struct NodeTile: View {
    let ready: Bool
    var size: CGFloat

    var body: some View {
        let tint: Color = ready ? .green : .secondary
        Image(systemName: "server.rack")
            .font(.system(size: size * 0.46, weight: .medium))
            .foregroundStyle(tint)
            .frame(width: size, height: size)
            .background(tint.opacity(0.15), in: .rect(cornerRadius: size * 0.27))
            .overlay(RoundedRectangle(cornerRadius: size * 0.27).strokeBorder(tint.opacity(0.25)))
            .accessibilityHidden(true)
    }
}

/// A thin usage bar; empty when the share is unknown.
struct UsageBar: View {
    let fraction: Double?
    let color: Color
    var height: CGFloat = 4

    var body: some View {
        Capsule()
            .fill(.fill.tertiary)
            .overlay(alignment: .leading) {
                GeometryReader { proxy in
                    if let fraction {
                        Capsule()
                            .fill(color)
                            .frame(width: max(height, proxy.size.width * fraction))
                    }
                }
            }
            .frame(height: height)
            .animation(.default, value: fraction)
    }
}

extension ClusterNode {
    /// "controller", "worker".
    var roleLabel: String { isController ? "controller" : "worker" }

    /// "worker · arm64": the list's second line. Capabilities live on the
    /// node's own screen, where there is room for them.
    var summary: String {
        [roleLabel, arch].compactMap(\.self).joined(separator: " · ")
    }
}

/// "v1.36.3+k3s1" as the header shows it: "k3s v1.36.3".
func kubernetesLabel(_ kubelet: String) -> String {
    guard let plus = kubelet.firstIndex(of: "+") else { return kubelet }
    // "k3s1" is the distribution and its build number: keep "k3s".
    let build = kubelet[kubelet.index(after: plus)...]
    let distribution = String(build.reversed().drop(while: \.isNumber).reversed())
    return distribution.isEmpty ? String(kubelet[..<plus]) : "\(distribution) \(kubelet[..<plus])"
}
