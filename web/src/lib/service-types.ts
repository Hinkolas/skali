// UI metadata for service kinds and statuses. Class strings are literal so
// Tailwind v4 can see them statically. This file is independent of the mock
// layer and survives the swap to the real API.

import type { ServiceKind, ServiceStatus } from '$lib/mock/types';

export const SERVICE_KIND_META: Record<
	ServiceKind,
	{ code: string; label: string; text: string; bg: string }
> = {
	application: {
		code: 'AP',
		label: 'Application',
		text: 'text-service-app',
		bg: 'bg-service-app/15'
	},
	database: { code: 'DB', label: 'Database', text: 'text-service-db', bg: 'bg-service-db/12' },
	cache: { code: 'CA', label: 'Cache', text: 'text-service-cache', bg: 'bg-service-cache/12' },
	storage: {
		code: 'ST',
		label: 'Storage',
		text: 'text-service-storage',
		bg: 'bg-service-storage/12'
	},
	ingress: { code: 'IN', label: 'Ingress', text: 'text-text-secondary', bg: 'bg-white/6' }
};

export const STATUS_META: Record<ServiceStatus, { label: string; dot: string; text: string }> = {
	running: { label: 'Running', dot: 'bg-status-success', text: 'text-status-success' },
	ready: { label: 'Ready', dot: 'bg-status-success', text: 'text-status-success' },
	syncing: { label: 'Syncing', dot: 'bg-status-warning', text: 'text-status-warning' },
	stopped: { label: 'Stopped', dot: 'bg-text-ghost', text: 'text-text-muted' }
};
