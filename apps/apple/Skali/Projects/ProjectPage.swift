import SkaliKit
import SwiftUI

extension View {
    /// The overview's bar: its large title with a line under it, plus
    /// everything `projectChrome` adds.
    func projectPage(
        _ model: ProjectModel, title: String, subtitle: String, dot: Color? = nil, followsStatus: Bool = false
    ) -> some View {
        navigationTitle(title)
            // Explicit: an automatic mode inherits the page it was pushed
            // from, and the service pages are inline.
            .navigationBarTitleDisplayMode(.large)
            // The bar's middle holds the environment, not the title.
            .largeSubtitle(subtitle, dot: dot, inline: false)
            .projectChrome(model, followsStatus: followsStatus)
    }

    /// The bar of a page the overview leads to (deployments, backups): an
    /// inline bar holding only the environment, plus everything
    /// `projectChrome` adds. The page puts its `ProjectPageHeader` at the
    /// top of its list instead of a large title. On iOS 27.1 (iPhone Duo),
    /// pushing a large title with a principal item from a scrolled overview
    /// never finishes laying out: the push spins the main thread for good.
    func projectSubpage(_ model: ProjectModel, title: String, also: (() async -> Void)? = nil) -> some View {
        navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .projectChrome(model, also: also)
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

/// The top of a `projectSubpage`'s list, in place of the large title its
/// bar cannot show: the page's tile as the overview links to it, its name,
/// and a line under it, as a service page starts.
struct ProjectPageHeader: View {
    let title: String
    let systemImage: String
    let tint: Color
    let detail: String

    @ScaledMetric(relativeTo: .largeTitle) private var tile: CGFloat = 58

    var body: some View {
        Section {
            HStack(spacing: 16) {
                ResourceTile(systemImage: systemImage, tint: tint, size: tile)
                VStack(alignment: .leading, spacing: 6) {
                    Text(title)
                        .font(.largeTitle.bold())
                        .lineLimit(1)
                        .minimumScaleFactor(0.6)
                    Text(detail)
                        .font(.subheadline.monospaced())
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityElement(children: .combine)
            .accessibilityAddTraits(.isHeader)
        }
        .listRowBackground(Color.clear)
        .listRowInsets(EdgeInsets(top: 0, leading: 4, bottom: 8, trailing: 4))
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
                    DeletingEnvironmentView(environment: environment, model: model)
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
