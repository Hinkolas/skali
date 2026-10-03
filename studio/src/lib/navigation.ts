// Single source of truth for app navigation. Consumed by the Sidebar
// variants and the in-page service tab bar (ServiceTabs).

import type { Component } from 'svelte';
import type { IconProps } from '@lucide/svelte';
import type { ServiceType } from '$lib/service-types';

import LayoutDashboard from '@lucide/svelte/icons/layout-dashboard';
import FolderKanban from '@lucide/svelte/icons/folder-kanban';
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
	/**
	 * A page that has not shipped yet. It stays listed so it comes back with
	 * a one-line change, but navigation leaves it out until its route exists.
	 */
	planned?: boolean;
	/** Hidden from users without the admin instance role. */
	adminOnly?: boolean;
}

export const ORG_NAV: { section: string; items: NavItemDef[] }[] = [
	{
		section: 'Organization',
		items: [
			{ label: 'Dashboard', slug: 'dashboard', icon: LayoutDashboard, planned: true },
			{ label: 'Projects', slug: 'projects', icon: FolderKanban }
		]
	},
	{
		section: 'Operations',
		items: [
			{ label: 'Alerts', slug: 'alerts', icon: Bell, planned: true },
			{ label: 'Backups', slug: 'backups', icon: Archive, planned: true }
		]
	},
	{
		section: 'Administration',
		items: [
			{ label: 'Users', slug: 'users', icon: Users, adminOnly: true },
			{ label: 'System', slug: 'system', icon: Settings2, adminOnly: true }
		]
	}
];

export const PROJECT_TABS: NavItemDef[] = [
	{ label: 'Overview', slug: '', icon: LayoutDashboard },
	{ label: 'Service graph', slug: 'graph', icon: Workflow, planned: true },
	{ label: 'Activity', slug: 'activity', icon: Activity, planned: true },
	{ label: 'Logs', slug: 'logs', icon: ScrollText, planned: true },
	{ label: 'Backups', slug: 'backups', icon: Archive },
	{ label: 'Settings', slug: 'settings', icon: Settings2 }
];

export const SERVICE_TABS: Record<ServiceType, NavItemDef[]> = {
	application: [
		{ label: 'Overview', slug: '', icon: LayoutDashboard },
		{ label: 'Deployments', slug: 'deployments', icon: Rocket },
		{ label: 'Metrics', slug: 'metrics', icon: Gauge },
		{ label: 'Logs', slug: 'logs', icon: ScrollText, planned: true },
		{ label: 'Environment', slug: 'environment', icon: KeyRound, planned: true },
		{ label: 'Scaling', slug: 'scaling', icon: Scaling, planned: true },
		{ label: 'Config', slug: 'config', icon: Settings2 }
	],
	database: [
		{ label: 'Overview', slug: '', icon: LayoutDashboard },
		{ label: 'Studio', slug: 'studio', icon: Table, planned: true },
		{ label: 'Metrics', slug: 'metrics', icon: Gauge, planned: true },
		{ label: 'Access', slug: 'access', icon: KeyRound, planned: true },
		{ label: 'Config', slug: 'config', icon: Settings2 }
	],
	bucket: [
		{ label: 'Overview', slug: '', icon: LayoutDashboard },
		{ label: 'Metrics', slug: 'metrics', icon: Gauge, planned: true },
		{ label: 'Config', slug: 'config', icon: Settings2 }
	]
};

/** The items that have a page, for rendering navigation. */
export function shipped<T extends NavItemDef>(items: T[]): T[] {
	return items.filter((item) => !item.planned);
}
