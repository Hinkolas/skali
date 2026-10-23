import SkaliKit
import SwiftUI

/// One project in a list: name and environment on the first line, the
/// service kinds, count and last activity under it, health on the right.
struct ProjectRow: View {
    let project: Project

    var body: some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 8) {
                    Text(project.label)
                        .font(.title3.weight(.semibold))
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
        .padding(.vertical, 6)
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
        HStack(spacing: 6) {
            if let counts = project.summary?.serviceCounts {
                ForEach(ServiceKind.allCases, id: \.self) { kind in
                    let count = counts.count(of: kind)
                    if count > 0 {
                        ServiceKindBadge(kind: kind, count: count)
                    }
                }
            }
            Text(detailLine)
                .font(.footnote.monospaced())
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .padding(.leading, 2)
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
