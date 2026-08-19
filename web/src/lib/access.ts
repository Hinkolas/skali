// Client-side reading of the access model: the server enforces every rule,
// these helpers only decide what the console disables and how it explains
// the refusal it would get. See docs/permissions.md.

import type { AuthUser } from '$lib/types/auth';
import type { AccessRole } from '$lib/types/project';

export const ROLE_RANK: Record<AccessRole, number> = {
	none: 0,
	read: 1,
	deploy: 2,
	maintain: 3,
	admin: 4
};

/** The ladder in ascending order, for pickers. */
export const ROLES: AccessRole[] = ['none', 'read', 'deploy', 'maintain', 'admin'];

/** Project roles: a membership is never `none` (remove it instead). */
export const PROJECT_ROLES: AccessRole[] = ['read', 'deploy', 'maintain', 'admin'];

export const ROLE_HINT: Record<AccessRole, string> = {
	none: 'Locked: listed by name only.',
	read: 'See status, logs, values names, revisions.',
	deploy: 'Deploy unchanged definitions, promote, roll back, restart, back up.',
	maintain: 'Change the definition and values, restore, exec.',
	admin: 'Settings, members, delete.'
};

export function roleAtLeast(role: AccessRole | undefined | null, min: AccessRole): boolean {
	if (!role) return false;
	return ROLE_RANK[role] >= ROLE_RANK[min];
}

/** The server's refusal wording, for titles on disabled controls. */
export function requiredTitle(
	min: AccessRole,
	scope: 'project' | 'environment',
	name: string
): string {
	return `${min} on ${scope} ${name} required`;
}

export function isInstanceAdmin(user: AuthUser | null | undefined): boolean {
	return user?.role === 'admin';
}

export function canCreateProject(user: AuthUser | null | undefined): boolean {
	return !!user && (user.role === 'admin' || user.create_projects);
}

export const CREATE_PROJECTS_TITLE = 'creating projects requires the create_projects permission';
