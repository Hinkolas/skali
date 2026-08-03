// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

export type SourceMode = 'managed' | 'file';

export type EnvironmentState = 'active' | 'down' | 'releasing';

export type ServiceHealth = 'unknown' | 'progressing' | 'healthy' | 'degraded' | 'unhealthy';

export interface Project {
	id: string;
	name: string;
	display_name: string;
	source_mode: SourceMode;
	created_at: string;
	updated_at: string;
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
	state: EnvironmentState;
	health: ServiceHealth;
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
	created_at: string;
}
