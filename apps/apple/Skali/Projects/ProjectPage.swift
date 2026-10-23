import SkaliKit
import SwiftUI

extension View {
    /// What every page of a project shares: its title with a line under
    /// it, plus everything `projectChrome` adds.
    func projectPage(
        _ model: ProjectModel, title: String, subtitle: String, dot: Color? = nil, monospaced: Bool = false,
        followsStatus: Bool = false, also: (() async -> Void)? = nil
    ) -> some View {
        navigationTitle(title)
            // Explicit: an automatic mode inherits the page it was pushed
            // from, and the service pages are inline.
            .navigationBarTitleDisplayMode(.large)
            // The bar's middle holds the environment, not the title.
            .largeSubtitle(subtitle, dot: dot, inline: false, monospaced: monospaced)
            .projectChrome(model, followsStatus: followsStatus, also: also)
    }

    /// The environment picker in the middle of the bar, and the project's
    /// change stream re-reading what is shown while the page is on screen.
    /// The environment's usage and runs are read whenever it changes;
    /// `followsStatus` also streams its live status, and `also` re-reads
    /// what only this page shows.
    func projectChrome(
        _ model: ProjectModel, followsStatus: Bool = false, also: (() async -> Void)? = nil
    ) -> some View {
        modifier(ProjectChromeModifier(model: model, followsStatus: followsStatus, also: also))
    }
}

private struct ProjectChromeModifier: ViewModifier {
    let model: ProjectModel
    let followsStatus: Bool
    let also: (() async -> Void)?

    func body(content: Content) -> some View {
        content
            .toolbar {
                if let environments = model.environments, let environment = model.environment {
                    ToolbarItem(placement: .principal) {
                        EnvironmentPicker(environments: environments, selected: environment, health: model.health) {
                            model.environmentName = $0.name
                        }
                    }
                }
            }
            .task { await model.followChanges(also: also) }
            .task(id: model.environment?.id) {
                guard model.environmentIsReadable, let id = model.environment?.id else { return }
                async let usage: Void = model.loadEnvironmentIfStale()
                if followsStatus { await model.followStatus(of: id) }
                await usage
            }
            .refreshable {
                await model.refresh()
                await also?()
            }
    }
}

/// Shows `content` for the environment on screen once it can be read; the
/// project being deleted, a locked or deleting environment, no
/// environments at all, and loading or failing say so instead.
struct EnvironmentGate<Content: View>: View {
    let model: ProjectModel
    @ViewBuilder let content: (ProjectEnvironment) -> Content

    var body: some View {
        if let ended = model.ended {
            ContentUnavailableView(
                ended == .deleted ? "Project Deleted" : "No Access",
                systemImage: ended == .deleted ? "trash" : "lock",
                description: Text(ended == .deleted
                                  ? "\(model.project.label) no longer exists."
                                  : "You no longer have access to \(model.project.label)."))
        } else if model.project.deletingAt != nil {
            ContentUnavailableView {
                Label("Deleting Project", systemImage: "trash")
            } description: {
                Text("Its environments are being deleted; the project goes with the last of them.")
            } actions: {
                ProgressView()
            }
        } else if let environments = model.environments {
            if let environment = model.environment {
                if environment.isLocked {
                    ContentUnavailableView(
                        "Environment Locked", systemImage: "lock",
                        description: Text("You can see that \(environment.name) exists, but not what runs in it. A project admin can give you access."))
                } else if environment.state == "releasing" {
                    ContentUnavailableView {
                        Label("Deleting \(environment.name)", systemImage: "trash")
                    } description: {
                        Text("The environment and its data are being deleted.")
                    } actions: {
                        ProgressView()
                    }
                } else {
                    content(environment)
                }
            } else if environments.isEmpty {
                ContentUnavailableView(
                    "No Environments", systemImage: "square.stack.3d.up.slash",
                    description: Text("Run skali deploy in the project's repository to create its first environment."))
            }
        } else if let error = model.loadError {
            ContentUnavailableView {
                Label("Project Unavailable", systemImage: "exclamationmark.triangle")
            } description: {
                Text(error)
            } actions: {
                Button("Try Again") { Task { await model.refresh() } }
            }
        } else {
            ProgressView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }
}
