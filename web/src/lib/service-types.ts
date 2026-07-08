// UI metadata for service kinds and statuses. Class strings are literal so
// Tailwind v4 can see them statically. This file is independent of the mock
// layer and survives the swap to the real API.

import type { ServiceKind, ServiceStatus } from '$lib/mock/types';
import type { ContainerKind, ContainerState, NodeRole, NodeStatus } from '$lib/types/nodes';
import type { AssignmentPhase, WorkloadStatus } from '$lib/types/workloads';

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

export const CONTAINER_STATE_META: Record<
	ContainerState,
	{ label: string; dot: string; text: string }
> = {
	created: { label: 'Created', dot: 'bg-text-ghost', text: 'text-text-muted' },
	running: { label: 'Running', dot: 'bg-status-success', text: 'text-status-success' },
	paused: { label: 'Paused', dot: 'bg-status-warning', text: 'text-status-warning' },
	restarting: { label: 'Restarting', dot: 'bg-status-warning', text: 'text-status-warning' },
	removing: { label: 'Removing', dot: 'bg-status-warning', text: 'text-status-warning' },
	exited: { label: 'Exited', dot: 'bg-text-ghost', text: 'text-text-muted' },
	dead: { label: 'Dead', dot: 'bg-status-danger', text: 'text-status-danger' },
	gone: { label: 'Gone', dot: 'bg-text-ghost', text: 'text-text-faint' }
};

export const CONTAINER_KIND_META: Record<ContainerKind, { text: string; bg: string }> = {
	application: { text: 'text-service-app', bg: 'bg-service-app/12' },
	database: { text: 'text-service-db', bg: 'bg-service-db/12' },
	system: { text: 'text-text-muted', bg: 'bg-white/6' }
};

export const CONTAINER_HEALTH_META: Record<'starting' | 'healthy' | 'unhealthy', string> = {
	starting: 'text-status-warning',
	healthy: 'text-status-success',
	unhealthy: 'text-status-danger'
};

export const WORKLOAD_STATUS_META: Record<
	WorkloadStatus,
	{ label: string; dot: string; text: string }
> = {
	running: { label: 'Running', dot: 'bg-status-success', text: 'text-status-success' },
	stopped: { label: 'Stopped', dot: 'bg-text-ghost', text: 'text-text-muted' },
	converging: { label: 'Converging', dot: 'bg-status-warning', text: 'text-status-warning' },
	importing: { label: 'Importing', dot: 'bg-status-warning', text: 'text-status-warning' },
	degraded: { label: 'Degraded', dot: 'bg-status-danger', text: 'text-status-danger' },
	deleting: { label: 'Deleting', dot: 'bg-text-ghost', text: 'text-text-faint' }
};

export const ASSIGNMENT_PHASE_META: Record<
	AssignmentPhase,
	{ label: string; dot: string; text: string }
> = {
	pending: { label: 'Pending', dot: 'bg-text-ghost', text: 'text-text-muted' },
	unschedulable: { label: 'Unschedulable', dot: 'bg-status-danger', text: 'text-status-danger' },
	pulling: { label: 'Pulling', dot: 'bg-status-warning', text: 'text-status-warning' },
	deploying: { label: 'Deploying', dot: 'bg-status-warning', text: 'text-status-warning' },
	stopping: { label: 'Stopping', dot: 'bg-status-warning', text: 'text-status-warning' },
	ready: { label: 'Ready', dot: 'bg-status-success', text: 'text-status-success' },
	stopped: { label: 'Stopped', dot: 'bg-text-ghost', text: 'text-text-muted' },
	removing: { label: 'Removing', dot: 'bg-text-ghost', text: 'text-text-faint' }
};
