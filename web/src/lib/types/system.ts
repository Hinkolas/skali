// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

import type { Observation } from './status';

export interface SystemMeta {
	version: string;
	name?: string;
}

export interface SystemObservation {
	mode: 'connected' | 'api-only';
	ready: boolean;
	observation: Observation;
	sources: ({ name: string } & Observation)[];
	kinds: { kind: string; synced: boolean }[];
	queue_depth: number;
	workers: number;
}
