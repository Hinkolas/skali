// UI metadata for service kinds and statuses. Class strings are literal so
// Tailwind v4 can see them statically. This file is independent of the mock
// layer and survives the swap to the real API.

import type { NodeRole, NodeStatus, ServiceKind, ServiceStatus } from '$lib/mock/types';
import type { NavIcon } from '$lib/navigation';

import Container from '@lucide/svelte/icons/container';
import Database from '@lucide/svelte/icons/database';
import DatabaseZap from '@lucide/svelte/icons/database-zap';
import Globe from '@lucide/svelte/icons/globe';
import HardDrive from '@lucide/svelte/icons/hard-drive';

export const SERVICE_KIND_META: Record<
	ServiceKind,
	{ icon: NavIcon; label: string; text: string; bg: string }
> = {
	application: {
		icon: Container,
		label: 'Application',
		text: 'text-service-app',
		bg: 'bg-service-app/15'
	},
	database: { icon: Database, label: 'Database', text: 'text-service-db', bg: 'bg-service-db/12' },
	cache: {
		icon: DatabaseZap,
		label: 'Cache',
		text: 'text-service-cache',
		bg: 'bg-service-cache/12'
	},
	storage: {
		icon: HardDrive,
		label: 'Storage',
		text: 'text-service-storage',
		bg: 'bg-service-storage/12'
	},
	ingress: { icon: Globe, label: 'Ingress', text: 'text-text-secondary', bg: 'bg-white/6' }
};

export const STATUS_META: Record<ServiceStatus, { label: string; dot: string; text: string }> = {
	running: { label: 'Running', dot: 'bg-status-success', text: 'text-status-success' },
	ready: { label: 'Ready', dot: 'bg-status-success', text: 'text-status-success' },
	syncing: { label: 'Syncing', dot: 'bg-status-warning', text: 'text-status-warning' },
	stopped: { label: 'Stopped', dot: 'bg-text-ghost', text: 'text-text-muted' }
};

export const NODE_STATE_META: Record<NodeStatus, { label: string; dot: string; text: string }> = {
	online: { label: 'Online', dot: 'bg-status-success', text: 'text-status-success' },
	offline: { label: 'Offline', dot: 'bg-text-ghost', text: 'text-text-muted' }
};

export const NODE_ROLE_META: Record<NodeRole, { text: string; bg: string }> = {
	master: { text: 'text-accent-light', bg: 'bg-accent/15' },
	edge: { text: 'text-service-app', bg: 'bg-service-app/12' },
	worker: { text: 'text-text-muted', bg: 'bg-white/6' },
	builder: { text: 'text-service-storage', bg: 'bg-service-storage/12' }
};
