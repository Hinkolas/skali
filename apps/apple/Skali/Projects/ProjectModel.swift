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

    /// The project's snapshots on the backup target, read by the Backups
    /// page.
    private(set) var snapshots: SnapshotsState = .unknown

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

    /// Names for the user ids runs record.
    var directory: [String: DirectoryUser] { session.directory }
    var userID: String { session.user.id }
    var isInstanceAdmin: Bool { session.user.isAdmin }

    /// The project's page in the Studio, on the environment shown.
    func studioURL(_ page: String? = nil) -> URL {
        var url = session.account.address.studioURL.appending(path: "projects/\(initial.name)")
        if let page { url.append(path: page) }
        if let environment { url.append(queryItems: [URLQueryItem(name: "env", value: environment.name)]) }
        return url
    }

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
        lastRefresh = .now
    }

    /// When everything was last read: a page opening right after another
    /// one read it skips the read its stream's `ready` asks for.
    private var lastRefresh: Date?

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
        environmentLoads[id] = .now
    }

    /// The same, unless a page read it moments ago: pages opening one
    /// after another do not each read it again.
    func loadEnvironmentIfStale() async {
        guard let id = environment?.id else { return }
        if let loaded = environmentLoads[id], Date.now.timeIntervalSince(loaded) < 5 { return }
        await loadEnvironment()
    }

    @ObservationIgnored private var environmentLoads: [String: Date] = [:]

    /// Follows the project's change signals until the screen goes away:
    /// every (re)connect and every change re-reads what is shown. Without
    /// the stream the screen still loads once.
    /// `also` re-reads what only the page on screen shows (the snapshots).
    func followChanges(also: (() async -> Void)? = nil) async {
        await session.loadDirectory()
        var delay = Duration.seconds(1)
        while !Task.isCancelled {
            do {
                for try await change in api.projectChanges(projectID: initial.id) {
                    switch change {
                    case .ready where lastRefresh.map({ Date.now.timeIntervalSince($0) < 5 }) == true:
                        await also?()
                    case .ready, .changed:
                        await refresh()
                        await also?()
                        delay = .seconds(1)
                    case .deleted, .revoked:
                        ended = change
                        return
                    }
                }
            } catch {
                if session.handleUnauthorized(error) { return }
                if environments == nil {
                    await refresh()
                    await also?()
                }
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

    /// The project's snapshots, or why the backup target refuses to list
    /// them.
    func loadSnapshots() async {
        do {
            snapshots = .loaded(try await api.snapshots(projectID: initial.id))
        } catch is CancellationError {
        } catch let error as APIError {
            if session.handleUnauthorized(error) { return }
            snapshots = .refused(SnapshotsRefusal(error))
        } catch {
            snapshots = .refused(SnapshotsRefusal(code: nil, status: nil, message: error.localizedDescription))
        }
    }

    // MARK: Actions

    /// Recreates every application's pods with what is deployed.
    func restartApplications() async throws {
        guard let id = environment?.id else { return }
        _ = try await session.gated { try await self.api.restartEnvironment(environmentID: id) }
        await loadEnvironment()
    }

    /// Snapshots the environment on screen; returns the run to follow.
    func startBackup() async throws -> String {
        guard let id = environment?.id else { throw CancellationError() }
        let started = try await session.gated { try await self.api.startBackup(environmentID: id) }
        await loadEnvironment()
        return started.runId
    }

    /// Restores a snapshot into an environment; returns the run to follow.
    func restore(_ snapshot: BackupSnapshot, into environment: ProjectEnvironment) async throws -> String {
        let runID = try await session.gated {
            try await self.api.restore(environmentID: environment.id, snapshotID: snapshot.id)
        }
        await loadEnvironment()
        return runID
    }

    func deleteSnapshot(_ snapshot: BackupSnapshot) async throws {
        try await session.gated { try await self.api.deleteSnapshot(projectID: self.initial.id, snapshotID: snapshot.id) }
        if case .loaded(let list) = snapshots {
            snapshots = .loaded(list.filter { $0.id != snapshot.id })
        }
    }

    /// Sets or (with nil) turns off the environment's automatic backups.
    func setBackupSchedule(_ schedule: BackupSchedule?, for environment: ProjectEnvironment) async throws {
        let updated = try await session.gated {
            try await self.api.setBackupSchedule(environmentID: environment.id, schedule)
        }
        if let index = environments?.firstIndex(where: { $0.id == updated.id }) {
            environments?[index] = updated
        }
    }

    func cancelRun(_ run: Run) async throws {
        try await session.gated { try await self.api.cancelRun(id: run.id) }
    }
}

/// What the Backups page knows about the project's snapshots.
enum SnapshotsState {
    case unknown
    case loaded([BackupSnapshot])
    case refused(SnapshotsRefusal)
}

/// Why snapshots could not be listed, in the words the page uses.
struct SnapshotsRefusal {
    let code: String?
    let status: Int?
    let message: String

    init(code: String?, status: Int?, message: String) {
        self.code = code
        self.status = status
        self.message = message
    }

    init(_ error: APIError) {
        self.init(code: error.code, status: error.status, message: error.localizedDescription)
    }

    /// No target is configured; an instance admin can set one.
    var isUnconfigured: Bool { code == "backup_target_unconfigured" }
    var isUnreachable: Bool { code == "backup_target_unreachable" }
    /// The daemon has no backups route at all (API-only mode).
    var isUnsupported: Bool { status == 404 && code == nil }
}
