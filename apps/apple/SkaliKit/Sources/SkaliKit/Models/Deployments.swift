import Foundation

/// Where a deployment's revision comes from, among the ones the apps
/// start: another environment's running revision (a promotion), or the
/// environment's own, re-run with its current values (a redeploy). Both
/// reuse artifacts by construction, so nothing builds on the phone.
public enum DeploySource: Hashable, Sendable {
    case promotion(fromEnvironmentID: String)
    case redeploy
}

/// The server's preview of a deployment (`POST /v1/environments/{id}/plan`):
/// what would change, and whether anything would.
public struct DeployPlan: Codable, Hashable, Sendable {
    public let warnings: [DeploymentWarning]?
    public let plan: PlanDocument?
    public let actions: [ArtifactAction]?
    /// The environment already runs exactly this; deploying changes nothing.
    public let upToDate: Bool
    /// The role the deployment needs on the environment.
    public let requiredRole: String?

    public init(
        warnings: [DeploymentWarning]? = nil, plan: PlanDocument?, actions: [ArtifactAction]? = nil,
        upToDate: Bool, requiredRole: String? = "deploy"
    ) {
        self.warnings = warnings
        self.plan = plan
        self.actions = actions
        self.upToDate = upToDate
        self.requiredRole = requiredRole
    }
}

/// The services and values a deployment changes, as the CLI prints them.
public struct PlanDocument: Codable, Hashable, Sendable {
    public let project: String?
    public let changes: [PlanChange]?
    public let values: [ValueChange]?

    public init(project: String? = nil, changes: [PlanChange]? = nil, values: [ValueChange]? = nil) {
        self.project = project
        self.changes = changes
        self.values = values
    }
}

/// What a deployment does about one application's artifact: `reuse`
/// for promotions and redeploys, `build` or `import` otherwise.
public struct ArtifactAction: Codable, Hashable, Sendable {
    public let application: String
    public let action: String
    public let kind: String?

    public init(application: String, action: String, kind: String? = nil) {
        self.application = application
        self.action = action
        self.kind = kind
    }
}

/// Advice the compiler attaches to a definition (`diagnostic.Warning`).
public struct DeploymentWarning: Codable, Hashable, Sendable {
    public let code: String
    public let message: String

    public init(code: String, message: String) {
        self.code = code
        self.message = message
    }
}

/// The answer to opening a deployment (`POST /v1/environments/{id}/deployments`):
/// the open window, or that there was nothing to deploy.
public struct OpenedDeployment: Codable, Hashable, Sendable {
    public let deployment: DeploymentRef?
    public let actions: [ArtifactAction]?
    public let upToDate: Bool?

    public init(deployment: DeploymentRef?, actions: [ArtifactAction]? = nil, upToDate: Bool? = nil) {
        self.deployment = deployment
        self.actions = actions
        self.upToDate = upToDate
    }
}

public struct DeploymentRef: Codable, Hashable, Sendable {
    public let id: String
    public let runId: String?

    public init(id: String, runId: String?) {
        self.id = id
        self.runId = runId
    }
}

/// How starting a deployment ended.
public enum DeployOutcome: Hashable, Sendable {
    /// The run that rolls it out, to follow.
    case started(runID: String)
    /// The environment already ran it; nothing was opened.
    case upToDate
}

/// The body of a plan or an open request.
struct DeployRequest: Encodable {
    let fromEnvironmentId: String?
    let redeploy: Bool?
    let allowDestructive: Bool?

    init(_ source: DeploySource, allowDestructive: Bool = false) {
        switch source {
        case .promotion(let id):
            fromEnvironmentId = id
            redeploy = nil
        case .redeploy:
            fromEnvironmentId = nil
            redeploy = true
        }
        self.allowDestructive = allowDestructive ? true : nil
    }
}
