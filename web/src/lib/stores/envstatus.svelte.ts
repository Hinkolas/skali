// Live environment status, fed by the status SSE stream alone: the stream's
// first event is the full document, so the project layout no longer waits
// on a status request before painting. One instance app-wide; the project
// layout drives sync() from an $effect (browser only). Consumers read
// `envStatus.doc` and show a pending state while `envStatus.pending` holds,
// instead of rendering the absence of a document as unknown health or an
// empty workload.

import { openStream, type StreamHandle, type StreamState } from '$lib/sse';
import type { EnvironmentStatus, ServiceStatus } from '$lib/types/status';

class EnvStatusStore {
	doc = $state<EnvironmentStatus | null>(null);
	streamState = $state<StreamState>('closed');
	private envId = $state<string | null>(null);

	private handle: StreamHandle | null = null;

	/**
	 * True between opening an environment's stream and its first document:
	 * nothing is known yet, which is not the same as knowing nothing runs.
	 * A stream that keeps failing stays pending; the rest of the console
	 * fails alongside it, since the same daemon serves both.
	 */
	get pending(): boolean {
		return this.envId !== null && this.doc === null;
	}

	/**
	 * Idempotent per environment: a repeat call for the same id keeps the
	 * live stream. Switching environments closes the old stream first; late
	 * events from it are dropped by the id guard.
	 */
	sync(envId: string): void {
		if (this.envId === envId) return;
		this.stop();
		this.envId = envId;
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
