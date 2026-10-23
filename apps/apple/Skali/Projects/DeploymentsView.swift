import SkaliKit
import SwiftUI

/// The environment's run history by day: its rollouts (deploys,
/// rollbacks, restarts, stops), or every run. A run opens to its own page,
/// which follows it live.
struct DeploymentsView: View {
    let model: ProjectModel

    @Environment(\.openURL) private var openURL
    @AppStorage("deployments.filter") private var filter: RunFilter = .rollouts

    var body: some View {
        EnvironmentGate(model: model) { environment in
            history(environment)
        }
        .projectPage(model, title: "Deployments", subtitle: subtitle, monospaced: true)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    Picker("Show", selection: $filter) {
                        Label("Rollouts", systemImage: "paperplane").tag(RunFilter.rollouts)
                        Label("All Runs", systemImage: "list.bullet").tag(RunFilter.all)
                    }
                    .pickerStyle(.inline)
                    Section {
                        Button("Open in Studio", systemImage: "safari") { openURL(model.studioURL("deployments")) }
                    }
                } label: {
                    Label("More", systemImage: "ellipsis")
                }
            }
        }
    }

    private var runs: [Run] {
        (model.runs ?? []).filter(filter.includes)
    }

    /// "rollouts · last deployed 19d ago", or what is under way.
    private var subtitle: String {
        let shown = filter == .rollouts ? "rollouts" : "all runs"
        guard let all = model.runs else { return shown }
        if let active = all.activeRun { return "\(shown) · \(active.kindLabel) in progress" }
        if let last = all.lastSucceededDeployment {
            return "\(shown) · last deployed \(compactAge(since: last.finishedAt ?? last.createdAt)) ago"
        }
        return "\(shown) · not deployed yet"
    }

    @ViewBuilder private func history(_ environment: ProjectEnvironment) -> some View {
        if model.runs == nil {
            ProgressView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if runs.isEmpty {
            ContentUnavailableView(
                filter == .rollouts ? "No Deploys Yet" : "No Runs Yet", systemImage: "paperplane",
                description: Text(filter == .rollouts
                                  ? "Deploys, rollbacks, restarts and stops of \(environment.name) appear here."
                                  : "Every run of \(environment.name) appears here."))
        } else {
            List {
                ForEach(runs.groupedByDay(), id: \.day) { group in
                    Section {
                        ForEach(group.runs) { run in
                            NavigationLink(value: ProjectRoute.run(run.id)) {
                                RunRow(run: run, directory: model.directory)
                            }
                        }
                    } header: {
                        Text(group.day, format: .dateTime.month(.abbreviated).day().year())
                    }
                }
            }
        }
    }
}
