// The mock dataset behind the UI prototype — values mirror the design draft
// (Skali App.dc.html) so the implementation can be compared side-by-side.

import type { Deployment, Org, Project, ProjectGraph, Service } from './types';
import type { Node } from '$lib/types/nodes';

export const ORG: Org = {
	id: 'org_01',
	slug: 'acme-cloud',
	name: 'acme-cloud',
	project_count: 3,
	node_count: 3,
	alert_count: 2,
	version: 'v0.6.1'
};

export const PROJECTS: Project[] = [
	{
		id: 'prj_storefront',
		slug: 'storefront',
		name: 'storefront',
		environment: 'production',
		status: 'healthy',
		service_count: 5,
		service_badges: [
			{ kind: 'application', count: 2 },
			{ kind: 'database' },
			{ kind: 'cache' },
			{ kind: 'storage' }
		],
		deploy_note: '12m ago',
		subtitle: 'Live · 5 services · last deploy 12 min ago',
		services_summary: '4 healthy · 1 syncing',
		stats: [
			{
				label: 'REQUESTS',
				value: '1.4k',
				unit: '/min',
				chip: { text: '↗ +12%', tone: 'success' },
				note: 'vs last hour'
			},
			{
				label: 'CLUSTER CPU',
				value: '8.2',
				unit: '%',
				chip: { text: '→ flat', tone: 'neutral' },
				note: '3 nodes'
			},
			{
				label: 'MEMORY',
				value: '2.1',
				unit: '/ 12 GB',
				chip: { text: '↘ −3%', tone: 'success' },
				note: 'last 7 days'
			},
			{
				label: 'ACTIVE ALERTS',
				value: '1',
				chip: { text: 'media sync', tone: 'warning' },
				note: '0 critical'
			}
		]
	},
	{
		id: 'prj_internal_tools',
		slug: 'internal-tools',
		name: 'internal-tools',
		environment: 'staging',
		status: 'healthy',
		service_count: 3,
		service_badges: [{ kind: 'application', count: 2 }, { kind: 'database' }],
		deploy_note: '2d ago',
		subtitle: 'Live · 3 services · last deploy 2 days ago',
		services_summary: '3 healthy',
		stats: [
			{
				label: 'REQUESTS',
				value: '86',
				unit: '/min',
				chip: { text: '→ flat', tone: 'neutral' },
				note: 'vs last hour'
			},
			{
				label: 'CLUSTER CPU',
				value: '2.4',
				unit: '%',
				chip: { text: '→ flat', tone: 'neutral' },
				note: '3 nodes'
			},
			{
				label: 'MEMORY',
				value: '0.9',
				unit: '/ 12 GB',
				chip: { text: '→ flat', tone: 'neutral' },
				note: 'last 7 days'
			},
			{
				label: 'ACTIVE ALERTS',
				value: '0',
				chip: { text: 'all clear', tone: 'success' },
				note: '0 critical'
			}
		]
	},
	{
		id: 'prj_landing',
		slug: 'landing',
		name: 'landing',
		environment: 'production',
		status: 'building',
		service_count: 2,
		service_badges: [{ kind: 'application' }, { kind: 'storage' }],
		building_note: 'building #48…',
		subtitle: 'Building · 2 services · deploy #48 in progress',
		services_summary: '1 healthy · 1 building',
		stats: [
			{
				label: 'REQUESTS',
				value: '312',
				unit: '/min',
				chip: { text: '↗ +4%', tone: 'success' },
				note: 'vs last hour'
			},
			{
				label: 'CLUSTER CPU',
				value: '1.1',
				unit: '%',
				chip: { text: '→ flat', tone: 'neutral' },
				note: '3 nodes'
			},
			{
				label: 'MEMORY',
				value: '0.4',
				unit: '/ 12 GB',
				chip: { text: '→ flat', tone: 'neutral' },
				note: 'last 7 days'
			},
			{
				label: 'ACTIVE ALERTS',
				value: '1',
				chip: { text: 'build #48', tone: 'warning' },
				note: '0 critical'
			}
		]
	}
];

const STOREFRONT_WEB_DEPLOYMENTS: Deployment[] = [
	{
		build: '#142',
		message: 'Merge pull request #38 from kotapet',
		sha: 'a3f9c21',
		trigger: 'push · main',
		duration: '3.2s',
		status: 'live',
		status_label: 'live · 12m'
	},
	{
		build: '#141',
		message: 'fix: cart badge count on hydration',
		sha: '8c1d0ef',
		trigger: 'push · main',
		duration: '2.8s',
		status: 'superseded',
		status_label: 'superseded'
	},
	{
		build: '#140',
		message: 'feat: product gallery zoom',
		sha: '02b77aa',
		trigger: 'manual · cli',
		duration: '4.1s',
		status: 'superseded',
		status_label: 'superseded'
	}
];

export const SERVICES: Service[] = [
	{
		id: 'svc_storefront_web',
		slug: 'storefront-web',
		name: 'storefront-web',
		project_slug: 'storefront',
		type: 'application',
		status: 'running',
		kind_label: 'application',
		node: 'node-01',
		image: 'ghcr.io/acme/web:1.42',
		domain: 'storefront.acme.dev',
		repo: 'acme/storefront',
		branch: 'main',
		start_command: 'node server.js',
		port: '3000 → :443',
		instances: 2,
		instance_nodes: 'node-01, node-02',
		autoscaled: false,
		cpu_pct: '4%',
		mem: '312M',
		health_check: 'GET /healthz · 200 · 34ms',
		connected_service_slugs: ['postgres-main', 'cache', 'media'],
		deployments: STOREFRONT_WEB_DEPLOYMENTS
	},
	{
		id: 'svc_storefront_api',
		slug: 'storefront-api',
		name: 'storefront-api',
		project_slug: 'storefront',
		type: 'application',
		status: 'running',
		kind_label: 'application',
		node: 'node-02',
		image: 'ghcr.io/acme/api:1.42',
		endpoint: 'api.internal:8080',
		repo: 'acme/storefront',
		branch: 'main',
		start_command: './api serve',
		port: '8080',
		instances: 3,
		instance_nodes: 'node-01, node-02, node-03',
		autoscaled: true,
		cpu_pct: '11%',
		mem: '648M',
		health_check: 'GET /healthz · 200 · 12ms',
		connected_service_slugs: ['postgres-main', 'cache'],
		deployments: STOREFRONT_WEB_DEPLOYMENTS
	},
	{
		id: 'svc_postgres_main',
		slug: 'postgres-main',
		name: 'postgres-main',
		project_slug: 'storefront',
		type: 'database',
		status: 'ready',
		kind_label: 'database · pg 17',
		node: 'node-02',
		engine: 'postgresql',
		version: '17',
		short_id: '9fe7af2a-8bde',
		created_at: 'jun 13, 2026',
		storage_used: '4.2',
		storage_total: '10',
		storage_pct: 42,
		connections: 14,
		max_connections: 100,
		compute: '0.5 vCPU · 1G',
		mem: '1.1G',
		last_backup: { time: '9:17', note: '✓ 4.2G snapshot' },
		connection: {
			host: 'pg-main.storefront.internal',
			port: '5432',
			database: 'storefront',
			user: 'storefront_rw',
			password: 'mock-not-a-real-password',
			url: 'postgresql://storefront_rw:mock-not-a-real-password@pg-main.storefront.internal:5432/storefront'
		},
		connected_apps: [
			{ slug: 'storefront-web', name: 'storefront-web', env_var: 'DATABASE_URL' },
			{ slug: 'storefront-api', name: 'storefront-api', env_var: 'DATABASE_URL' }
		]
	},
	{
		id: 'svc_cache',
		slug: 'cache',
		name: 'cache',
		project_slug: 'storefront',
		type: 'cache',
		status: 'ready',
		kind_label: 'cache · valkey 8',
		node: 'node-01',
		engine: 'valkey',
		version: '8',
		endpoint: 'cache.internal:6379',
		hit_rate: '98.2%',
		keys: '41k',
		mem_used: '64M',
		mem_total: '256M'
	},
	{
		id: 'svc_media',
		slug: 'media',
		name: 'media',
		project_slug: 'storefront',
		type: 'storage',
		status: 'syncing',
		kind_label: 'storage · s3',
		node: 'node-01, node-03',
		engine: 's3',
		size: '18.4G',
		objects: '12,408',
		egress_per_day: '2.1G/d'
	},

	/* internal-tools */
	{
		id: 'svc_tools_web',
		slug: 'tools-web',
		name: 'tools-web',
		project_slug: 'internal-tools',
		type: 'application',
		status: 'running',
		kind_label: 'application',
		node: 'node-02',
		image: 'ghcr.io/acme/tools-web:0.9',
		domain: 'tools.acme.dev',
		repo: 'acme/internal-tools',
		branch: 'main',
		start_command: 'node build/index.js',
		port: '3000 → :443',
		instances: 1,
		instance_nodes: 'node-02',
		autoscaled: false,
		cpu_pct: '2%',
		mem: '188M',
		health_check: 'GET /healthz · 200 · 21ms',
		connected_service_slugs: ['postgres-tools'],
		deployments: [
			{
				build: '#31',
				message: 'chore: bump deps',
				sha: 'f21bc03',
				trigger: 'push · main',
				duration: '2.1s',
				status: 'live',
				status_label: 'live · 2d'
			}
		]
	},
	{
		id: 'svc_tools_api',
		slug: 'tools-api',
		name: 'tools-api',
		project_slug: 'internal-tools',
		type: 'application',
		status: 'running',
		kind_label: 'application',
		node: 'node-02',
		image: 'ghcr.io/acme/tools-api:0.9',
		endpoint: 'tools-api.internal:8080',
		repo: 'acme/internal-tools',
		branch: 'main',
		start_command: './tools-api',
		port: '8080',
		instances: 1,
		instance_nodes: 'node-02',
		autoscaled: false,
		cpu_pct: '1%',
		mem: '96M',
		health_check: 'GET /healthz · 200 · 9ms',
		connected_service_slugs: ['postgres-tools'],
		deployments: [
			{
				build: '#31',
				message: 'chore: bump deps',
				sha: 'f21bc03',
				trigger: 'push · main',
				duration: '1.8s',
				status: 'live',
				status_label: 'live · 2d'
			}
		]
	},
	{
		id: 'svc_postgres_tools',
		slug: 'postgres-tools',
		name: 'postgres-tools',
		project_slug: 'internal-tools',
		type: 'database',
		status: 'ready',
		kind_label: 'database · pg 17',
		node: 'node-02',
		engine: 'postgresql',
		version: '17',
		short_id: '3c1d99e0-a412',
		created_at: 'may 2, 2026',
		storage_used: '0.8',
		storage_total: '5',
		storage_pct: 16,
		connections: 3,
		max_connections: 50,
		compute: '0.25 vCPU · 512M',
		mem: '210M',
		last_backup: { time: '9:17', note: '✓ 0.8G snapshot' },
		connection: {
			host: 'pg-tools.internal-tools.internal',
			port: '5432',
			database: 'tools',
			user: 'tools_rw',
			password: 'mock-not-a-real-password',
			url: 'postgresql://tools_rw:mock-not-a-real-password@pg-tools.internal-tools.internal:5432/tools'
		},
		connected_apps: [
			{ slug: 'tools-web', name: 'tools-web', env_var: 'DATABASE_URL' },
			{ slug: 'tools-api', name: 'tools-api', env_var: 'DATABASE_URL' }
		]
	},

	/* landing */
	{
		id: 'svc_landing_web',
		slug: 'landing-web',
		name: 'landing-web',
		project_slug: 'landing',
		type: 'application',
		status: 'running',
		kind_label: 'application',
		node: 'node-01',
		image: 'ghcr.io/acme/landing:1.7',
		domain: 'acme.dev',
		repo: 'acme/landing',
		branch: 'main',
		start_command: 'node server.js',
		port: '3000 → :443',
		instances: 1,
		instance_nodes: 'node-01',
		autoscaled: false,
		cpu_pct: '1%',
		mem: '142M',
		health_check: 'GET /healthz · 200 · 18ms',
		connected_service_slugs: ['assets'],
		deployments: [
			{
				build: '#48',
				message: 'feat: pricing page refresh',
				sha: '5d80cc1',
				trigger: 'push · main',
				duration: '—',
				status: 'failed',
				status_label: 'building…'
			},
			{
				build: '#47',
				message: 'fix: og image dimensions',
				sha: '91e2ab7',
				trigger: 'push · main',
				duration: '2.4s',
				status: 'live',
				status_label: 'live · 5d'
			}
		]
	},
	{
		id: 'svc_assets',
		slug: 'assets',
		name: 'assets',
		project_slug: 'landing',
		type: 'storage',
		status: 'ready',
		kind_label: 'storage · s3',
		node: 'node-01',
		engine: 's3',
		size: '2.3G',
		objects: '1,204',
		egress_per_day: '0.4G/d'
	}
];

export const NODES: Node[] = [
	{
		id: '0195b6a0-0000-7000-8000-000000000001',
		name: 'node-01',
		roles: ['master', 'worker'],
		advertise_addr: '10.0.0.11:7443',
		public_addr: null,
		arch: 'amd64',
		os: 'linux',
		skalid_version: '0.1.0',
		status: 'online',
		last_seen: '2026-07-01T12:00:00Z',
		metrics: null,
		created_at: '2026-05-01T09:00:00Z',
		updated_at: '2026-07-01T12:00:00Z'
	},
	{
		id: '0195b6a0-0000-7000-8000-000000000002',
		name: 'node-02',
		roles: ['worker', 'edge'],
		advertise_addr: '10.0.0.12:7443',
		public_addr: '203.0.113.10:443',
		arch: 'amd64',
		os: 'linux',
		skalid_version: '0.1.0',
		status: 'online',
		last_seen: '2026-07-01T12:00:00Z',
		metrics: null,
		created_at: '2026-05-02T09:00:00Z',
		updated_at: '2026-07-01T12:00:00Z'
	},
	{
		id: '0195b6a0-0000-7000-8000-000000000003',
		name: 'node-03',
		roles: ['worker'],
		advertise_addr: '10.0.0.13:7443',
		public_addr: null,
		arch: 'arm64',
		os: 'linux',
		skalid_version: '0.1.0',
		status: 'online',
		last_seen: '2026-07-01T12:00:00Z',
		metrics: null,
		created_at: '2026-05-03T09:00:00Z',
		updated_at: '2026-07-01T12:00:00Z'
	}
];

/* Graph layout: positions/segments copied from the design draft. */
export const GRAPHS: Record<string, ProjectGraph> = {
	storefront: {
		width: 1150,
		height: 640,
		nodes: [
			{
				slug: 'ingress',
				service_slug: null,
				x: 40,
				y: 190,
				w: 250,
				kind: 'ingress',
				title: 'edge ingress',
				subtitle: 'networking · built-in',
				mono: 'storefront.acme.dev',
				chips: [{ text: 'tls auto', tone: 'success' }, { text: 'http/2' }],
				drawer: {
					type_line: 'networking · built-in',
					rows: [
						{ k: 'route', v: 'storefront.acme.dev' },
						{ k: 'tls', v: 'auto · letsencrypt' },
						{ k: 'target', v: 'storefront-web:3000' },
						{ k: 'protocol', v: 'http/2 · gzip' },
						{ k: 'req rate', v: '1.4k/min' }
					]
				}
			},
			{
				slug: 'web',
				service_slug: 'storefront-web',
				x: 430,
				y: 100,
				w: 270,
				kind: 'application',
				title: 'storefront-web',
				subtitle: 'application',
				mono: 'ghcr.io/acme/web:1.42',
				chips: [{ text: '×2' }, { text: 'cpu 4%' }, { text: '312M' }],
				status: 'running',
				drawer: {
					type_line: 'application',
					rows: [
						{ k: 'image', v: 'ghcr.io/acme/web:1.42' },
						{ k: 'instances', v: '2 · node-01, node-02' },
						{ k: 'port', v: '3000' },
						{ k: 'cpu / mem', v: '4% · 312M' },
						{ k: 'deploy', v: '#142 · 12m ago' }
					]
				}
			},
			{
				slug: 'api',
				service_slug: 'storefront-api',
				x: 430,
				y: 320,
				w: 270,
				kind: 'application',
				title: 'storefront-api',
				subtitle: 'application',
				mono: 'ghcr.io/acme/api:1.42',
				chips: [{ text: '×3 auto' }, { text: 'cpu 11%' }, { text: '648M' }],
				status: 'running',
				drawer: {
					type_line: 'application',
					rows: [
						{ k: 'image', v: 'ghcr.io/acme/api:1.42' },
						{ k: 'instances', v: '3 · auto-scaled' },
						{ k: 'port', v: '8080' },
						{ k: 'cpu / mem', v: '11% · 648M' },
						{ k: 'deploy', v: '#142 · 12m ago' }
					]
				}
			},
			{
				slug: 'db',
				service_slug: 'postgres-main',
				x: 860,
				y: 80,
				w: 260,
				kind: 'database',
				title: 'postgres-main',
				subtitle: 'database · pg 17',
				chips: [{ text: '4.2/10G' }, { text: 'conns 14' }],
				status: 'ready',
				drawer: {
					type_line: 'database · postgresql 17',
					rows: [
						{ k: 'endpoint', v: 'pg-main.storefront.internal:5432' },
						{ k: 'storage', v: '4.2G / 10G' },
						{ k: 'connections', v: '14 / 100' },
						{ k: 'node', v: 'node-02' },
						{ k: 'backup', v: 'today 09:17 · ok' }
					]
				}
			},
			{
				slug: 'cache',
				service_slug: 'cache',
				x: 860,
				y: 260,
				w: 260,
				kind: 'cache',
				title: 'cache',
				subtitle: 'cache · valkey 8',
				chips: [{ text: 'hits 98.2%' }, { text: '41k keys' }],
				status: 'ready',
				drawer: {
					type_line: 'cache · valkey 8',
					rows: [
						{ k: 'endpoint', v: 'cache.storefront.internal:6379' },
						{ k: 'memory', v: '64M / 256M' },
						{ k: 'hit rate', v: '98.2%' },
						{ k: 'keys', v: '41,203' },
						{ k: 'node', v: 'node-01' }
					]
				}
			},
			{
				slug: 'media',
				service_slug: 'media',
				x: 860,
				y: 440,
				w: 260,
				kind: 'storage',
				title: 'media',
				subtitle: 'storage · s3',
				chips: [{ text: 'syncing…', tone: 'warning' }, { text: '18.4G' }],
				status: 'syncing',
				drawer: {
					type_line: 'storage · s3-compatible',
					rows: [
						{ k: 'endpoint', v: 'media.storefront.internal:9000' },
						{ k: 'size', v: '18.4G · 12,408 objects' },
						{ k: 'egress', v: '2.1G / day' },
						{ k: 'replication', v: 'syncing → node-03' },
						{ k: 'node', v: 'node-01, node-03' }
					]
				}
			}
		],
		segments: [
			{ left: 290, top: 260, width: 70 },
			{ left: 360, top: 175, height: 85 },
			{ left: 360, top: 175, width: 70 },
			{ left: 565, top: 250, height: 70 },
			{ left: 700, top: 395, width: 80 },
			{ left: 780, top: 150, height: 360 },
			{ left: 780, top: 150, width: 80 },
			{ left: 780, top: 330, width: 80 },
			{ left: 780, top: 510, width: 80 }
		],
		labels: [
			{ text: ':443', left: 296, top: 249 },
			{ text: ':8080', left: 538, top: 272 },
			{ text: ':5432', left: 790, top: 138 },
			{ text: ':6379', left: 790, top: 318 },
			{ text: ':9000', left: 790, top: 498 }
		]
	}
};
