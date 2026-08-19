// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

export type SourceMode = 'managed' | 'file';

export type EnvironmentState = 'active' | 'down' | 'releasing';

export type ServiceHealth = 'unknown' | 'progressing' | 'healthy' | 'degraded' | 'unhealthy';

/**
 * One step of the access ladder; each includes everything below it. `none`
 * locks an environment (listed by name, contents refused).
 */
export type AccessRole = 'none' | 'read' | 'deploy' | 'maintain' | 'admin';

/** The caller's standing on a project: membership role and effective role per environment name. */
export interface ProjectAccess {
	role: AccessRole;
	environments: Record<string, AccessRole>;
}

export interface EnvironmentSettings {
	max_role: AccessRole;
	deploy_policy: 'direct' | 'promote-only';
	promote_from: string[];
	priority: 'normal' | 'high';
}

/** One user's role on a project (membership) or an environment (cell). */
export interface Member {
	user_id: string;
	email: string;
	name: string;
	role: AccessRole;
}

export interface Project {
	id: string;
	name: string;
	display_name: string;
	source_mode: SourceMode;
	created_at: string;
	updated_at: string;
	access: ProjectAccess;
	/** Present only when the list was fetched with ?include=summary. */
	summary?: ProjectSummary;
}

export interface ProjectSummary {
	environments: SummaryEnvironment[];
	service_counts: ServiceCounts;
}

export interface SummaryEnvironment {
	id: string;
	name: string;
	access: AccessRole;
	/** Absent on a locked environment. */
	state?: EnvironmentState;
	health?: ServiceHealth;
}

export interface ServiceCounts {
	applications: number;
	databases: number;
	buckets: number;
}

export interface Environment {
	id: string;
	project_id: string;
	name: string;
	/** The caller's effective role; `none` marks a locked environment, which carries nothing else. */
	access: AccessRole;
	created_at?: string;
	settings?: EnvironmentSettings;
}
