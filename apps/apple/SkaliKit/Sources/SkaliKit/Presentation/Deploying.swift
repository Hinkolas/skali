import Foundation

// What the apps do to a whole environment, as the Studio's Actions menu
// does (studio/src/lib/components/shell/EnvActions.svelte): promoting and
// redeploying (run/PromoteModal.svelte, RedeployModal.svelte), stopping
// and deleting (lib/environments.ts), and the hold an incomplete restore
// keeps (run/IncompleteRestoreBanner.svelte).

extension ClusterAPI {
    /// Starts a promotion or a redeploy: opens the deployment, checks that
    /// every artifact is reused, and hands it to the daemon. Both reuse
    /// artifacts by construction; anything else would need a build the
    /// apps cannot run, so it fails the deployment instead of guessing.
    public func deploy(
        into environmentID: String, from source: DeploySource, allowDestructive: Bool = false
    ) async throws -> DeployOutcome {
        let opened = try await openDeployment(
            environmentID: environmentID, from: source, allowDestructive: allowDestructive)
        guard opened.upToDate != true, let deployment = opened.deployment else { return .upToDate }
        if let stray = (opened.actions ?? []).first(where: { $0.action != "reuse" }) {
            let error = DeployError.unexpectedArtifactWork(application: stray.application, action: stray.action)
            try? await failDeployment(id: deployment.id, reason: error.localizedDescription)
            throw error
        }
        return .started(runID: try await completeDeployment(id: deployment.id))
    }
}

public enum DeployError: Error, Hashable, LocalizedError {
    /// The server asked for a build or an import in a deployment that
    /// should only reuse artifacts.
    case unexpectedArtifactWork(application: String, action: String)

    public var errorDescription: String? {
        switch self {
        case .unexpectedArtifactWork(let application, let action):
            "The cluster wanted to \(action) \(application), which only the CLI can do. Nothing was deployed."
        }
    }
}

extension DeployPlan {
    public var changes: [PlanChange] { plan?.changes ?? [] }
    public var values: [ValueChange] { plan?.values ?? [] }

    /// Removes or destroys service state: confirmed by typing the
    /// environment's name, as in the CLI.
    public var isDestructive: Bool { changes.contains { $0.destructive == true } }
}

extension ProjectEnvironment {
    /// Promote-only: deploys land here only as promotions.
    public var isProtected: Bool { settings?.deployPolicy == "promote-only" }

    /// Why a promotion from `source` cannot land here, among what the
    /// listing shows; nil when it may be tried. The server has the last
    /// word when planning.
    public func promotionRefusal(from source: ProjectEnvironment) -> String? {
        if isLocked { return "locked for you" }
        if state == "releasing" { return "being deleted" }
        if role < .deploy { return "deploy access required" }
        if isProtected, let allowed = settings?.promoteFrom, !allowed.isEmpty, !allowed.contains(source.name) {
            return "accepts promotions from \(allowed.formatted(.list(type: .or))) only"
        }
        return nil
    }

    /// The environments `self` may be promoted to, pickable ones first and
    /// the last one used leading them; refused ones follow with their
    /// reasons, so the protection stays legible.
    public func promotionTargets(among environments: [ProjectEnvironment]) -> [ProjectEnvironment] {
        let others = environments.filter { $0.id != id }
        func rank(_ environment: ProjectEnvironment) -> Int {
            if environment.promotionRefusal(from: self) != nil { return 2 }
            return environment.name == lastPromotionTarget ? 0 : 1
        }
        return others.enumerated()
            .sorted { (rank($0.element), $0.offset) < (rank($1.element), $1.offset) }
            .map(\.element)
    }
}

/// Why the environment on screen cannot be promoted from, before the
/// sheet asks the server; nil when it may be tried.
public func promotionRefusal(
    source: ProjectEnvironment, environments: [ProjectEnvironment], status: EnvironmentStatus?
) -> String? {
    if !environments.contains(where: { $0.id != source.id }) { return "no other environments" }
    if source.isLocked { return "locked for you" }
    if let status, status.activeRevision == nil { return "nothing is running yet" }
    return nil
}

/// Why the environment cannot be redeployed, from what is on screen;
/// nil when it may be tried. One run at a time works on an environment.
public func redeployRefusal(_ environment: ProjectEnvironment, status: EnvironmentStatus?, runs: [Run]?) -> String? {
    if environment.role < .deploy { return "deploy access required" }
    if status?.incompleteRestore != nil { return "held by a restore" }
    if let status, status.activeRevision == nil { return "nothing is running yet" }
    if let active = runs?.first(where: \.isActive) { return "a \(active.kindLabel) is running" }
    return nil
}

/// Why the environment cannot be stopped; nil when it may be.
public func stopRefusal(_ environment: ProjectEnvironment) -> String? {
    if environment.role < .admin { return "admin access required" }
    if environment.state == "down" { return "already stopped" }
    return nil
}

/// Why the environment cannot be deleted; nil when it may be.
public func deleteRefusal(_ environment: ProjectEnvironment) -> String? {
    environment.role < .admin ? "admin access required" : nil
}

extension [Run] {
    /// The delete under way: the newest purge in flight, else any run in
    /// flight (the reconciler may not have adopted the decision yet).
    public var deletionRun: Run? {
        first { $0.kind == "purge" && $0.isActive } ?? first(where: \.isActive)
    }
}

extension RevisionRef {
    /// "ee121438": the checksum's first block, as the CLI shows it.
    public var shortChecksum: String {
        let digest = checksum.split(separator: ":").last.map(String.init) ?? checksum
        return String(digest.prefix(8))
    }
}

// MARK: Incomplete restores

extension IncompleteRestore {
    public var isRunning: Bool { status == "running" }

    /// "was cancelled", "failed".
    public var outcome: String { status == "cancelled" ? "was cancelled" : "failed" }

    /// What it set out to rewrite, by service.
    public var restoredComponents: [RestoreComponent] {
        (components ?? []).map(RestoreComponent.init)
    }
}

/// One thing a restore rewrites: a database, a bucket, or an
/// application's volume.
public struct RestoreComponent: Hashable, Sendable {
    public let kind: ServiceKind
    /// The service's key; for a volume, the application's.
    public let key: String
    /// The volume's name, for a volume.
    public let volume: String?

    /// `db:main`, `bucket:media`, `volume:web.data`.
    public init(_ component: String) {
        let parts = component.split(separator: ":", maxSplits: 1).map(String.init)
        let (prefix, name) = parts.count == 2 ? (parts[0], parts[1]) : ("", component)
        switch prefix {
        case "db":
            kind = .database
            key = name
            volume = nil
        case "bucket":
            kind = .bucket
            key = name
            volume = nil
        default:
            let dotted = name.split(separator: ".", maxSplits: 1).map(String.init)
            kind = .application
            key = dotted[0]
            volume = dotted.count == 2 ? dotted[1] : nil
        }
    }

    /// "main", "web/data".
    public var label: String { volume.map { "\(key)/\($0)" } ?? key }
}
