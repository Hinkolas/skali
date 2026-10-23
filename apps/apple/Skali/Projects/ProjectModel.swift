import Foundation
import Observation
import SkaliKit

/// One open project: its environments, definition and storage, and per
/// environment its live status, usage and runs. While the screen is on,
/// the status streams and the project's change signals trigger re-reads,
/// as in the Studio.
@Observable
final class ProjectModel {
    private let initial: Project
    private let session: ClusterSession

    private(set) var environments: [ProjectEnvironment]?
    private(set) var loadError: String?
    /// Nil until read, and for a project without a draft.
    private(set) var definition: ProjectDefinition?
    private(set) var storage: [ServiceStorage]?
    /// Set when the project went away while open: deleted, or no longer
    /// the caller's to see.
    private(set) var ended: ProjectChange?

    // Per environment id, so switching back shows the last state at once.
    private var statuses: [String: EnvironmentStatus] = [:]
    private var usage: [String: EnvironmentMetrics] = [:]
    private var runLists: [String: [Run]] = [:]

    init(project: Project, session: ClusterSession) {
        initial = project
        self.session = session
    }

    /// The listing's newest copy of the project.
    var project: Project {
        session.projects?.first { $0.id == initial.id } ?? initial
    }

    var api: any ClusterAPI { session.api }

    // MARK: The environment on screen

    /// The environment the person picked; kept on the session, so it
    /// survives leaving the project.
    var environmentName: String? {
        get { session.environmentChoices[initial.id] }
        set { session.environmentChoices[initial.id] = newValue }
    }

    var environment: ProjectEnvironment? {
        environments?.preferred(named: environmentName)
    }

    var status: EnvironmentStatus? { environment.flatMap { statuses[$0.id] } }
    var metrics: EnvironmentMetrics? { environment.flatMap { usage[$0.id] } }
    var runs: [Run]? { environment.flatMap { runLists[$0.id] } }

    /// Whether the environment's contents can be shown: not locked, not
    /// being deleted.
    var environmentIsReadable: Bool {
        guard let environment else { return false }
        return !environment.isLocked && environment.state != "releasing"
    }

    var serviceCount: Int {
        definition?.serviceCount ?? project.serviceCount
    }

    /// The line under the name: services, health and state.
    var summary: String {
        guard let environments else { return "" }
        guard !environments.isEmpty else { return "no environments yet" }
        guard environmentIsReadable else { return environment?.stateLabel ?? "locked" }
        return environmentSummary(serviceCount: serviceCount, status: status)
    }

    /// The dot beside the summary: the live verdict, the cached one until
    /// the first status arrives.
    var health: ServiceHealth? {
        guard environmentIsReadable else { return nil }
        return status?.overallHealth ?? environment?.health
    }

    // MARK: Loading

    /// Everything on screen: the project's documents and the current
    /// environment's usage and runs. When the read changes which
    /// environment is on screen (the first read picks one), the screen's
    /// per-environment task loads it instead.
    func refresh() async {
        let shown = environment?.id
        await loadProject()
        if environment?.id == shown { await loadEnvironment() }
    }

    private func loadProject() async {
        let api = api
        let id = initial.id
        async let storage = try? api.projectStorage(projectID: id)
        do {
            async let environments = api.environments(projectID: id)
            async let draft = api.projectDraft(projectID: id)
            let (environmentsValue, draftValue) = try await (environments, draft)
            self.environments = environmentsValue
            definition = draftValue?.definition
            loadError = nil
        } catch is CancellationError {
        } catch {
            if session.handleUnauthorized(error) { return }
            if environments == nil { loadError = error.localizedDescription }
        }
        if let storage = await storage { self.storage = storage }
    }

    /// Usage and runs of the environment on screen; its status streams.
    func loadEnvironment() async {
        guard environmentIsReadable, let id = environment?.id else { return }
        let api = api
        async let metrics = try? api.environmentMetrics(environmentID: id, window: .day)
        async let runs = try? api.runs(environmentID: id)
        if let metrics = await metrics { usage[id] = metrics }
        if let runs = await runs { runLists[id] = runs }
    }

    /// Follows the project's change signals until the screen goes away:
    /// every (re)connect and every change re-reads what is shown. Without
    /// the stream the screen still loads once.
    func followChanges() async {
        var delay = Duration.seconds(1)
        while !Task.isCancelled {
            do {
                for try await change in api.projectChanges(projectID: initial.id) {
                    switch change {
                    case .ready, .changed:
                        await refresh()
                        delay = .seconds(1)
                    case .deleted, .revoked:
                        ended = change
                        return
                    }
                }
            } catch {
                if session.handleUnauthorized(error) { return }
                if environments == nil { await refresh() }
            }
            try? await Task.sleep(for: delay)
            delay = min(delay * 2, .seconds(15))
        }
    }

    /// Streams the status of one environment until the screen leaves it;
    /// a dropped stream reconnects with backoff.
    func followStatus(of environmentID: String) async {
        var delay = Duration.seconds(1)
        while !Task.isCancelled {
            do {
                for try await status in api.environmentStatusUpdates(environmentID: environmentID) {
                    statuses[environmentID] = status
                    delay = .seconds(1)
                }
            } catch let error as APIError where error.status == 403 || error.status == 404 {
                return
            } catch {
                if session.handleUnauthorized(error) { return }
            }
            try? await Task.sleep(for: delay)
            delay = min(delay * 2, .seconds(15))
        }
    }

    // MARK: Actions

    /// Recreates every application's pods with what is deployed.
    func restartApplications() async throws {
        guard let id = environment?.id else { return }
        _ = try await session.gated { try await self.api.restartEnvironment(environmentID: id) }
        await loadEnvironment()
    }
}
