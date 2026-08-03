// Shapes for the mock service graph, the one surface still fed by static
// data (the graph page is deliberately excluded from the real-API swap).
// The shared unions live in their permanent homes and are imported here.

import type { ChipTone } from '$lib/models/view';
import type { ServiceKind, ServiceStatus } from '$lib/service-types';

export type { ChipTone, ServiceKind, ServiceStatus };

export interface GraphChip {
	text: string;
	tone?: ChipTone;
}

export interface GraphNodeData {
	/** Graph-local id (also drawer key). */
	slug: string;
	/** Routable service — null for built-ins like ingress. */
	service_slug: string | null;
	x: number;
	y: number;
	w: number;
	kind: ServiceKind;
	title: string;
	subtitle: string;
	mono?: string;
	chips: GraphChip[];
	status?: ServiceStatus;
	drawer: { type_line: string; rows: { k: string; v: string }[] };
}

/** Dashed connector piece; width => horizontal, height => vertical. */
export interface GraphSegment {
	left: number;
	top: number;
	width?: number;
	height?: number;
}

export interface GraphLabel {
	text: string;
	left: number;
	top: number;
}

export interface ProjectGraph {
	width: number;
	height: number;
	nodes: GraphNodeData[];
	segments: GraphSegment[];
	labels: GraphLabel[];
}
