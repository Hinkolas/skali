// Single source of truth for sidebar navigation. Consumed by the Sidebar
// variants and by the [section]/[tab] stub routes to 404 unknown slugs.

import type { Component } from 'svelte';
import type { IconProps } from '@lucide/svelte';
import type { ServiceType } from '$lib/mock/types';

import LayoutDashboard from '@lucide/svelte/icons/layout-dashboard';
import FolderKanban from '@lucide/svelte/icons/folder-kanban';
import Server from '@lucide/svelte/icons/server';
import Container from '@lucide/svelte/icons/container';
import Package from '@lucide/svelte/icons/package';
import Globe from '@lucide/svelte/icons/globe';
import Bell from '@lucide/svelte/icons/bell';
import Archive from '@lucide/svelte/icons/archive';
import Users from '@lucide/svelte/icons/users';
import Settings2 from '@lucide/svelte/icons/settings-2';
import Workflow from '@lucide/svelte/icons/workflow';
import Activity from '@lucide/svelte/icons/activity';
import ScrollText from '@lucide/svelte/icons/scroll-text';
import Rocket from '@lucide/svelte/icons/rocket';
import KeyRound from '@lucide/svelte/icons/key-round';
import Scaling from '@lucide/svelte/icons/scaling';
import Table from '@lucide/svelte/icons/table';
import Gauge from '@lucide/svelte/icons/gauge';

export type NavIcon = Component<IconProps, object, ''>;

export interface NavItemDef {
	label: string;
	/** URL segment appended to the context base; '' = the context's index page. */
	slug: string;
	icon: NavIcon;
	/** Only this slug has a designed page; everything else renders a stub. */
	stub?: boolean;
	/** Hidden from users without the admin instance role. */
	adminOnly?: boolean;
}

export const ORG_NAV: { section: string; items: NavItemDef[] }[] = [
	{
		section: 'Organization',
		items: [
			{ label: 'Dashboard', slug: 'dashboard', icon: LayoutDashboard, stub: true },
			{ label: 'Projects', slug: 'projects', icon: FolderKanban },
			{ label: 'Nodes', slug: 'nodes', icon: Server, adminOnly: true },
			{ label: 'Domains', slug: 'domains', icon: Globe, stub: true }
		]
	},
	{
		section: 'Operations',
		items: [
			{ label: 'Alerts', slug: 'alerts', icon: Bell, stub: true },
			{ label: 'Backups', slug: 'backups', icon: Archive, stub: true }
		]
	},
	{
		section: 'Administration',
		items: [
			{ label: 'Users', slug: 'users', icon: Users, adminOnly: true },
			// The raw engine surface: a debugging tool, deliberately away from
			// the product pages a member ever sees.
			{ label: 'Containers', slug: 'containers', icon: Container, adminOnly: true },
			// The mirror catalog: what the cluster registry serves.
			{ label: 'Registry', slug: 'registry', icon: Package, adminOnly: true },
			{ label: 'System', slug: 'system', icon: Settings2, stub: true, adminOnly: true }
		]
	}
];

export const PROJECT_TABS: NavItemDef[] = [
	{ label: 'Overview', slug: '', icon: LayoutDashboard },
	{ label: 'Service graph', slug: 'graph', icon: Workflow },
	{ label: 'Activity', slug: 'activity', icon: Activity, stub: true },
	{ label: 'Logs', slug: 'logs', icon: ScrollText, stub: true },
	{ label: 'Settings', slug: 'settings', icon: Settings2, stub: true }
];

export const SERVICE_TABS: Record<ServiceType, NavItemDef[]> = {
	application: [
		{ label: 'Overview', slug: '', icon: LayoutDashboard },
		{ label: 'Deployments', slug: 'deployments', icon: Rocket, stub: true },
		{ label: 'Logs', slug: 'logs', icon: ScrollText, stub: true },
		{ label: 'Environment', slug: 'environment', icon: KeyRound, stub: true },
		{ label: 'Domains', slug: 'domains', icon: Globe, stub: true },
		{ label: 'Scaling', slug: 'scaling', icon: Scaling, stub: true },
		{ label: 'Settings', slug: 'settings', icon: Settings2, stub: true }
	],
	database: [
		{ label: 'Overview', slug: '', icon: LayoutDashboard },
		{ label: 'Studio', slug: 'studio', icon: Table, stub: true },
		{ label: 'Backups', slug: 'backups', icon: Archive, stub: true },
		{ label: 'Metrics', slug: 'metrics', icon: Gauge, stub: true },
		{ label: 'Access', slug: 'access', icon: KeyRound, stub: true },
		{ label: 'Settings', slug: 'settings', icon: Settings2, stub: true }
	],
	cache: [
		{ label: 'Overview', slug: '', icon: LayoutDashboard },
		{ label: 'Metrics', slug: 'metrics', icon: Gauge, stub: true },
		{ label: 'Settings', slug: 'settings', icon: Settings2, stub: true }
	],
	storage: [
		{ label: 'Overview', slug: '', icon: LayoutDashboard },
		{ label: 'Metrics', slug: 'metrics', icon: Gauge, stub: true },
		{ label: 'Settings', slug: 'settings', icon: Settings2, stub: true }
	]
};

/** Org-level stub sections resolvable at /[section] ('projects' has its own route). */
export function findOrgSection(slug: string): NavItemDef | null {
	for (const group of ORG_NAV) {
		const item = group.items.find((i) => i.slug === slug && i.stub);
		if (item) return item;
	}
	return null;
}

export function findProjectTab(slug: string): NavItemDef | null {
	return PROJECT_TABS.find((t) => t.slug === slug && t.stub) ?? null;
}

export function findServiceTab(type: ServiceType, slug: string): NavItemDef | null {
	return SERVICE_TABS[type].find((t) => t.slug === slug && t.stub) ?? null;
}
