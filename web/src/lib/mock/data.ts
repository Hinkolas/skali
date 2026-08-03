// The mock service graph behind the (deliberately still mocked) graph page:
// positions mirror the design draft (Skali App.dc.html). Everything else the
// prototype mocked has been replaced by the real API.

import type { ProjectGraph } from './types';

/* Graph layout: positions/segments copied from the design draft, then scaled
   110% with the rest of the UI. Connector lengths are derived from scaled
   endpoints (not scaled independently) so the joints stay closed after
   rounding. Node cards size themselves from --spacing, so this grid has to
   move with it or the cards outgrow their gaps. */
export const GRAPHS: Record<string, ProjectGraph> = {
	storefront: {
		width: 1265,
		height: 704,
		nodes: [
			{
				slug: 'ingress',
				service_slug: null,
				x: 44,
				y: 209,
				w: 275,
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
				x: 473,
				y: 110,
				w: 297,
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
				x: 473,
				y: 352,
				w: 297,
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
				x: 946,
				y: 88,
				w: 286,
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
				x: 946,
				y: 286,
				w: 286,
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
				x: 946,
				y: 484,
				w: 286,
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
			{ left: 319, top: 286, width: 77 },
			{ left: 396, top: 193, height: 93 },
			{ left: 396, top: 193, width: 77 },
			{ left: 622, top: 275, height: 77 },
			{ left: 770, top: 435, width: 88 },
			{ left: 858, top: 165, height: 396 },
			{ left: 858, top: 165, width: 88 },
			{ left: 858, top: 363, width: 88 },
			{ left: 858, top: 561, width: 88 }
		],
		labels: [
			{ text: ':443', left: 326, top: 274 },
			{ text: ':8080', left: 592, top: 299 },
			{ text: ':5432', left: 869, top: 152 },
			{ text: ':6379', left: 869, top: 350 },
			{ text: ':9000', left: 869, top: 548 }
		]
	}
};
