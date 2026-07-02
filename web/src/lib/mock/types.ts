// Entity shapes for the mock data layer. snake_case mirrors the Go API
// convention (see lib/types/auth.ts) so the later swap to real endpoints is
// mechanical. Display-ready strings ('4.2G', '12m ago') are deliberate: this
// layer feeds a visual prototype, not real telemetry.

export type ServiceKind = 'application' | 'database' | 'cache' | 'storage' | 'ingress';
/** Kinds that exist as real, routable services (ingress is graph-only). */
export type ServiceType = Exclude<ServiceKind, 'ingress'>;

export type ServiceStatus = 'running' | 'ready' | 'syncing' | 'stopped';
export type ChipTone = 'success' | 'neutral' | 'warning';

export interface Org {
	id: string;
	slug: string;
	name: string;
	project_count: number;
	node_count: number;
	alert_count: number;
	version: string;
}

export interface StatCardData {
	label: string;
	value: string;
	unit?: string;
	chip?: { text: string; tone: ChipTone };
	note?: string;
	progress?: { pct: number; class: string };
}

export interface Project {
	id: string;
	slug: string;
	name: string;
	environment: 'production' | 'staging';
	status: 'healthy' | 'building' | 'degraded';
	service_count: number;
	/** Type badges on the org project card, e.g. AP ×2, DB, CA, ST. */
	service_badges: { kind: ServiceKind; count?: number }[];
	/** Card footer: 'deploy 12m ago' — or building_note instead. */
	deploy_note?: string;
	building_note?: string;
	/** Subtitle under the project page title. */
	subtitle: string;
	/** Services section subtitle, e.g. '4 healthy · 1 syncing'. */
	services_summary: string;
	stats: StatCardData[];
}

interface ServiceBase {
	id: string;
	slug: string;
	name: string;
	project_slug: string;
	status: ServiceStatus;
	/** Subtitle under the service name, e.g. 'database · pg 17'. */
	kind_label: string;
	node: string;
}

export interface Deployment {
	build: string;
	message: string;
	sha: string;
	trigger: string;
	duration: string;
	status: 'live' | 'superseded' | 'failed';
	status_label: string;
}

export interface ApplicationService extends ServiceBase {
	type: 'application';
	image: string;
	/** Public domain (web apps) — mutually exclusive-ish with internal endpoint. */
	domain?: string;
	endpoint?: string;
	repo: string;
	branch: string;
	start_command: string;
	port: string;
	instances: number;
	instance_nodes: string;
	autoscaled: boolean;
	cpu_pct: string;
	mem: string;
	health_check: string;
	connected_service_slugs: string[];
	deployments: Deployment[];
}

export interface DatabaseService extends ServiceBase {
	type: 'database';
	engine: string;
	version: string;
	short_id: string;
	created_at: string;
	storage_used: string;
	storage_total: string;
	storage_pct: number;
	connections: number;
	max_connections: number;
	compute: string;
	mem: string;
	last_backup: { time: string; note: string };
	connection: {
		host: string;
		port: string;
		database: string;
		user: string;
		password: string;
		url: string;
	};
	connected_apps: { slug: string; name: string; env_var: string }[];
}

export interface CacheService extends ServiceBase {
	type: 'cache';
	engine: string;
	version: string;
	endpoint: string;
	hit_rate: string;
	keys: string;
	mem_used: string;
	mem_total: string;
}

export interface StorageService extends ServiceBase {
	type: 'storage';
	engine: string;
	size: string;
	objects: string;
	egress_per_day: string;
}

export type Service = ApplicationService | DatabaseService | CacheService | StorageService;

export interface NodeInfo {
	name: string;
	address: string;
	provider: string;
	cpu_pct: string;
	memory: string;
	service_count: number;
	state: 'healthy' | 'degraded' | 'offline';
}

/* ── Service graph (positions copied from the design draft) ────────────── */

export interface GraphChip {
	text: string;
	tone?: ChipTone;
}

export interface GraphNodeData {
	/** Graph-local id (also drawer key). */
	slug: string;
	/** Routable service — null for built-ins like ingress. */
	service_slug: string | null;
	x: number;
	y: number;
	w: number;
	kind: ServiceKind;
	title: string;
	subtitle: string;
	mono?: string;
	chips: GraphChip[];
	status?: ServiceStatus;
	drawer: { type_line: string; rows: { k: string; v: string }[] };
}

/** Dashed connector piece; width => horizontal, height => vertical. */
export interface GraphSegment {
	left: number;
	top: number;
	width?: number;
	height?: number;
}

export interface GraphLabel {
	text: string;
	left: number;
	top: number;
}

export interface ProjectGraph {
	width: number;
	height: number;
	nodes: GraphNodeData[];
	segments: GraphSegment[];
	labels: GraphLabel[];
}
