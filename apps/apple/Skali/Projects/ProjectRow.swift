import SkaliKit
import SwiftUI

/// One project in a list: name and environment on the first line, the
/// service kinds, count and last activity under it, health on the right.
/// Sized like the system's two-line list rows (Mail, Files): a headline
/// over a caption line, at Dynamic Type's text styles.
struct ProjectRow: View {
    let project: Project

    var body: some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 5) {
                HStack(spacing: 6) {
                    Text(project.label)
                        .font(.headline)
                        .lineLimit(1)
                        .truncationMode(.tail)
                    environments
                }
                details
            }
            Spacer(minLength: 0)
            if project.deletingAt != nil {
                ProgressView()
                    .accessibilityLabel("Deleting")
            } else if project.summary != nil {
                HealthDot(health: project.worstHealth)
            }
        }
        .padding(.vertical, 2)
        .accessibilityElement(children: .combine)
    }

    @ViewBuilder private var environments: some View {
        let rollup = project.environmentRollup
        if project.deletingAt != nil {
            TagChip(text: "deleting")
        } else if let first = rollup.first {
            TagChip(text: first.name)
            if rollup.count > 1 {
                TagChip(text: "+\(rollup.count - 1)")
                    .accessibilityLabel("and \(rollup.count - 1) more")
            }
        }
    }

    private var details: some View {
        HStack(spacing: 4) {
            if let counts = project.summary?.serviceCounts {
                ForEach(ServiceKind.allCases, id: \.self) { kind in
                    let count = counts.count(of: kind)
                    if count > 0 {
                        ServiceKindBadge(kind: kind, count: count)
                    }
                }
            }
            Text(detailLine)
                .font(.caption.monospaced())
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .padding(.leading, 3)
        }
    }

    private var detailLine: String {
        let age = compactAge(since: project.lastActivity)
        guard project.summary != nil else { return age }
        switch project.serviceCount {
        case 0: return "no services · \(age)"
        case 1: return "1 service · \(age)"
        case let count: return "\(count) services · \(age)"
        }
    }
}

#Preview(traits: .sizeThatFitsLayout) {
    List(DemoCluster.projects.prefix(4)) { ProjectRow(project: $0) }
}
