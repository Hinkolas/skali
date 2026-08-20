// Shapes mirror the skali API deployments surface (plan / open / complete),
// the subset the console's promote flow reads.

export interface PlanChange {
	service: string;
	action: string;
	destructive?: boolean;
	detail?: string;
}

export interface PlanValueChange {
	name: string;
	action: string;
}

export interface PlanDocument {
	project: string;
	changes: PlanChange[];
	values: PlanValueChange[];
}

export interface ArtifactAction {
	application: string;
	action: string;
	kind: string;
	artifact_id: string;
	build_id: string;
	upstream: string;
	platform: string;
	reference: string;
}

export interface PlanResult {
	plan: PlanDocument | null;
	actions: ArtifactAction[];
	up_to_date: boolean;
	orphaned?: string[];
	required_role: string;
	bypass_protection: boolean;
}

export interface DeploymentRef {
	id: string;
	run_id?: string;
}

/** POST /deployments response; `deployment` is absent when up_to_date. */
export interface OpenedDeployment {
	deployment?: DeploymentRef;
	actions?: ArtifactAction[];
	up_to_date?: boolean;
	bypass_protection?: boolean;
}
