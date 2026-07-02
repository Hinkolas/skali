// Async accessors over the static mock dataset, shaped like the future API
// client so pages/loads don't change when real endpoints land — only these
// bodies do.

import { GRAPHS, NODES, ORG, PROJECTS, SERVICES } from './data';
import type { NodeInfo, Org, Project, ProjectGraph, Service } from './types';

export type * from './types';

export async function getOrg(): Promise<Org> {
	return ORG;
}

export async function listProjects(): Promise<Project[]> {
	return PROJECTS;
}

export async function getProject(slug: string): Promise<Project | null> {
	return PROJECTS.find((p) => p.slug === slug) ?? null;
}

export async function listServices(projectSlug: string): Promise<Service[]> {
	return SERVICES.filter((s) => s.project_slug === projectSlug);
}

export async function getService(
	projectSlug: string,
	serviceSlug: string
): Promise<Service | null> {
	return SERVICES.find((s) => s.project_slug === projectSlug && s.slug === serviceSlug) ?? null;
}

export async function listNodes(): Promise<NodeInfo[]> {
	return NODES;
}

export async function getProjectGraph(projectSlug: string): Promise<ProjectGraph | null> {
	return GRAPHS[projectSlug] ?? null;
}
