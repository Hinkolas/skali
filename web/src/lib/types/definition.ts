// The compiled project definition, mirroring internal/compiler/types.go.
// Unlike the API envelopes this document is camelCase: it is the canonical
// compiler output embedded verbatim in draft and revision payloads.

export interface ProjectDefinition {
	version: string;
	name: string;
	description?: string;
	applications?: Record<string, Application>;
	databases?: Record<string, DatabaseClaim>;
	buckets?: Record<string, BucketClaim>;
	backups?: Record<string, Backup>;
	requiredVariables?: VariableRequirement[];
	dependencies?: Record<string, string[]>;
}

export interface VariableRequirement {
	name: string;
	required: boolean;
	default?: string;
	hasDefault?: boolean;
}

export interface Expression {
	parts: ExpressionPart[];
}

export interface ExpressionPart {
	kind: 'literal' | 'project_variable' | 'service_output';
	value?: string;
	name?: string;
	default?: string;
	hasDefault?: boolean;
	collection?: string;
	service?: string;
	output?: string;
	sensitive?: boolean;
}

export interface Application {
	source: ApplicationSource;
	command?: string[];
	environment?: Record<string, Expression>;
	ports?: Record<string, PortDef>;
	routes?: Record<string, Route>;
	health?: HealthChecks;
	resources?: Resources;
	scaling: Scaling;
	placement?: Placement;
	deployment?: DeploymentConfig;
	shutdown?: Shutdown;
	volumes?: Record<string, Volume>;
}

export interface ApplicationSource {
	kind: string;
	image?: string;
	build?: Build;
}

export interface Build {
	context: string;
	dockerfile: string;
	target?: string;
	arguments?: Record<string, string>;
}

export interface PortDef {
	port: number;
	protocol: string;
}

export interface PortTarget {
	name?: string;
	number?: number;
}

export interface Route {
	domain: Expression;
	path: string;
	port: PortTarget;
	tls: string;
}

export interface HealthChecks {
	startup?: Probe;
	readiness?: Probe;
	liveness?: Probe;
}

export interface Probe {
	http?: HTTPProbe;
	intervalMillis?: number;
	timeoutMillis?: number;
	failureThreshold?: number;
}

export interface HTTPProbe {
	port: PortTarget;
	path: string;
}

export interface Resources {
	requests?: ResourceValues;
	limits?: ResourceValues;
}

export interface ResourceValues {
	milliCpu?: number;
	memoryBytes?: number;
	temporaryStorageBytes?: number;
}

export interface Scaling {
	minReplicas: number;
	maxReplicas: number;
	cpuTargetUtilization?: number;
}

export interface Placement {
	spreadAcross?: string;
	minimum?: number;
	enforcement?: string;
}

export interface DeploymentConfig {
	releaseCommand?: ReleaseCommand;
	rollout?: Rollout;
}

export interface ReleaseCommand {
	command?: string[];
	timeoutMillis?: number;
}

export interface Rollout {
	strategy?: string;
	maxUnavailable?: number;
	maxSurge?: number;
	timeoutMillis?: number;
}

export interface Shutdown {
	gracePeriodMillis?: number;
}

export interface Volume {
	mountPath: string;
	sizeBytes: number;
}

export interface DatabaseClaim {
	engine: string;
	version: string;
	isolation: string;
	availability: string;
	storageBytes?: number;
	extensions?: string[];
	pointInTimeRecoverySeconds?: number;
}

export interface BucketClaim {
	visibility: string;
	storageQuotaBytes?: number;
	objectQuota?: number;
	maxObjectSizeBytes?: number;
	versioning: string;
	abortIncompleteUploadsAfterSeconds?: number;
	expireNoncurrentVersionsAfterSeconds?: number;
}

export interface Backup {
	schedule: string;
	retentionSeconds: number;
	include: Selection;
}

export interface Selection {
	allDatabases?: boolean;
	databases?: string[];
	allBuckets?: boolean;
	buckets?: string[];
	allVolumes?: boolean;
	volumes?: string[];
}

export interface DraftResponse {
	draft: {
		version: number;
		format: string;
		source: string;
		hash: string;
		definition: ProjectDefinition;
	};
}

/**
 * Render an expression for display: literals verbatim, everything dynamic as
 * a ${...} placeholder. Sensitive parts never render their value (they do
 * not carry one anyway).
 */
export function renderExpression(expr: Expression | undefined): string {
	if (!expr?.parts?.length) return '';
	return expr.parts
		.map((part) => {
			switch (part.kind) {
				case 'literal':
					return part.value ?? '';
				case 'project_variable':
					return `\${${part.name ?? 'var'}}`;
				case 'service_output':
					return `\${${[part.collection, part.service, part.output].filter(Boolean).join('.')}}`;
				default:
					return `\${${part.name ?? part.kind}}`;
			}
		})
		.join('');
}
