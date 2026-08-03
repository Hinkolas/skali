// Live environment status: seeded from the layout load, kept fresh by the
// status SSE stream. One instance app-wide; the project layout drives sync()
// from an $effect (browser only, so SSR never leaks state across requests).
// Consumers derive `envStatus.doc ?? data.status` so SSR renders the seed.

import { openStream, type StreamHandle, type StreamState } from '$lib/sse';
import type { EnvironmentStatus, ServiceStatus } from '$lib/types/status';

class EnvStatusStore {
	doc = $state<EnvironmentStatus | null>(null);
	streamState = $state<StreamState>('closed');

	private handle: StreamHandle | null = null;
	private envId: string | null = null;

	/**
	 * Idempotent per environment: a repeat call for the same id keeps the
	 * live stream (the stream is authoritative over a re-run load's seed).
	 * Switching environments closes the old stream first; late events from
	 * it are dropped by the id guard.
	 */
	sync(envId: string, seed: EnvironmentStatus | null): void {
		if (this.envId === envId) {
			if (!this.doc && seed) this.doc = seed;
			return;
		}
		this.stop();
		this.envId = envId;
		this.doc = seed;
		const id = envId;
		this.handle = openStream<EnvironmentStatus>({
			path: `/v1/environments/${envId}/status/stream`,
			events: 'status',
			onEvent: (doc) => {
				if (this.envId !== id) return;
				this.doc = doc;
			},
			onState: (state) => {
				if (this.envId !== id) return;
				this.streamState = state;
			}
		});
	}

	stop(): void {
		this.handle?.close();
		this.handle = null;
		this.envId = null;
		this.doc = null;
		this.streamState = 'closed';
	}

	/** The live status of one service, looked up by (type, key). */
	service(type: string, key: string): ServiceStatus | undefined {
		return this.doc?.services.find((s) => s.type === type && s.key === key);
	}
}

export const envStatus = new EnvStatusStore();
