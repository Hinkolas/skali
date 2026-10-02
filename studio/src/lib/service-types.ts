// UI metadata for service kinds, health, and statuses. Class strings are
// literal so Tailwind v4 can see them statically. This file is the home of
// the kind/status unions.

import type { NavIcon } from '$lib/navigation';
import type { ServiceHealth } from '$lib/types/project';

import Container from '@lucide/svelte/icons/container';
import Database from '@lucide/svelte/icons/database';
import HardDrive from '@lucide/svelte/icons/hard-drive';

/** All service kinds the platform serves. */
export type ServiceKind = 'application' | 'database' | 'bucket';

/** Alias kept for call sites that read better as "type" (service.type). */
export type ServiceType = ServiceKind;

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
	bucket: {
		icon: HardDrive,
		label: 'Bucket',
		text: 'text-service-storage',
		bg: 'bg-service-storage/12'
	}
};

/**
 * Service health from the status projection. `unknown` is a real state (the
 * daemon runs api-only or observation is stale), not an error.
 */
export const HEALTH_META: Record<ServiceHealth, { label: string; dot: string; text: string }> = {
	healthy: { label: 'Healthy', dot: 'bg-status-success', text: 'text-status-success' },
	progressing: { label: 'Progressing', dot: 'bg-status-warning', text: 'text-status-warning' },
	degraded: { label: 'Degraded', dot: 'bg-status-warning', text: 'text-status-warning' },
	unhealthy: { label: 'Unhealthy', dot: 'bg-status-danger', text: 'text-status-danger' },
	unknown: { label: 'Unknown', dot: 'bg-text-ghost', text: 'text-text-muted' }
};

/** Cluster node roles (GET /v1/nodes vocabulary: k3s server or agent). */
export type NodeRole = 'server' | 'agent';

export const NODE_STATE_META: Record<
	'online' | 'offline',
	{ label: string; dot: string; text: string }
> = {
	online: { label: 'Online', dot: 'bg-status-success', text: 'text-status-success' },
	offline: { label: 'Offline', dot: 'bg-text-ghost', text: 'text-text-muted' }
};

export const NODE_ROLE_META: Record<string, { text: string; bg: string }> = {
	server: { text: 'text-accent-light', bg: 'bg-accent/15' },
	agent: { text: 'text-text-muted', bg: 'bg-white/6' },
	// Capability chips share the role row on the nodes table.
	edge: { text: 'text-service-app', bg: 'bg-service-app/12' },
	builder: { text: 'text-service-storage', bg: 'bg-service-storage/12' },
	database: { text: 'text-service-db', bg: 'bg-service-db/12' }
};

/**
 * Storage category segments for the capacity bars, in render order. The
 * first three reuse the service kind colors so a bar segment and its kind
 * badge read as the same thing; free space is the bar's unfilled track.
 */
export const STORAGE_CATEGORY_META: { key: string; label: string; class: string }[] = [
	{ key: 'volumes_bytes', label: 'Volumes', class: 'bg-service-app' },
	{ key: 'databases_bytes', label: 'Databases', class: 'bg-service-db' },
	{ key: 'objects_bytes', label: 'Objects', class: 'bg-service-storage' },
	{ key: 'temporary_bytes', label: 'Temporary', class: 'bg-service-cache' },
	{ key: 'images_bytes', label: 'Images', class: 'bg-accent/70' },
	{ key: 'system_bytes', label: 'System', class: 'bg-white/25' }
];

/** Storage kind segments for the per-project bar, same palette. */
export const STORAGE_KIND_META: Record<string, { label: string; class: string }> = {
	volume: { label: 'Volumes', class: 'bg-service-app' },
	database: { label: 'Databases', class: 'bg-service-db' },
	bucket: { label: 'Buckets', class: 'bg-service-storage' },
	temporary: { label: 'Temporary', class: 'bg-service-cache' }
};
