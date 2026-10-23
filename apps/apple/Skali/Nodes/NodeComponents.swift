import SkaliKit
import SwiftUI

/// The server glyph on its tile, green while the node is ready.
struct NodeTile: View {
    let ready: Bool
    var size: CGFloat

    var body: some View {
        ResourceTile(systemImage: "server.rack", tint: ready ? .green : .secondary, size: size)
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

    /// Online, cordoned or offline, in its color.
    var statusCapsule: StatusCapsule {
        if !ready { return StatusCapsule(text: "Offline", tint: .red) }
        if !schedulable { return StatusCapsule(text: "Cordoned", tint: .orange) }
        return StatusCapsule(text: "Online", tint: .green)
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

/// "of 4 cores", "of 1 core", "of 500m".
func cpuCapacity(_ millicores: Int) -> String {
    switch millicores {
    case ..<1000: "of \(formatCores(millicores))"
    case 1000: "of 1 core"
    default: "of \(formatCores(millicores)) cores"
    }
}
