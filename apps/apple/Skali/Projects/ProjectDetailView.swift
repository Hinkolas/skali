import SkaliKit
import SwiftUI

/// A first look at a project: its environments and services. The full
/// project screens come next.
struct ProjectDetailView: View {
    let project: Project

    var body: some View {
        List {
            Section("Environments") {
                let environments = project.environmentRollup
                if environments.isEmpty {
                    Text("No environments yet").foregroundStyle(.secondary)
                }
                ForEach(environments) { environment in
                    HStack {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(environment.name)
                            if let state = environment.state {
                                Text(state).font(.footnote).foregroundStyle(.secondary)
                            }
                        }
                        Spacer()
                        if environment.access == "none" {
                            Image(systemName: "lock.fill").foregroundStyle(.secondary)
                                .accessibilityLabel("Locked")
                        } else if let health = environment.health {
                            Text(health.label).font(.subheadline).foregroundStyle(health.tint)
                        }
                    }
                }
            }
            if let counts = project.summary?.serviceCounts {
                Section("Services") {
                    ForEach(ServiceKind.allCases, id: \.self) { kind in
                        LabeledContent {
                            Text(counts.count(of: kind), format: .number)
                        } label: {
                            Label(kind.pluralLabel.capitalized, systemImage: kind.systemImage)
                                .foregroundStyle(kind.tint)
                        }
                    }
                }
            }
            Section {
                LabeledContent("Last Activity", value: project.lastActivity, format: .relative(presentation: .named))
                LabeledContent("Created", value: project.createdAt, format: .dateTime.day().month().year())
                LabeledContent("Your Role", value: project.access.role.capitalized)
            }
        }
        .navigationTitle(project.label)
        .navigationBarTitleDisplayMode(.inline)
    }
}
