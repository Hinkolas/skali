// Live run list of the selected environment, fed by the runs SSE stream.
// One stream per open project serves every consumer: the run feeds on the
// page (RunsSection) and the sidebar's in-progress mark. The project layout
// drives sync() next to the status stream. Consumers read of(envId) and use
// their load's seed until the first document arrives.

import { openStream, type StreamHandle } from '$lib/sse';
import { activeRun, type Run, type RunsList } from '$lib/types/runs';

class EnvRunsStore {
	private runs = $state<Run[] | null>(null);
	private envId = $state<string | null>(null);

	private handle: StreamHandle | null = null;

	/**
	 * Idempotent per environment, like envStatus.sync: a repeat call keeps
	 * the stream, and switching environments closes the old one first.
	 */
	sync(envId: string): void {
		if (this.envId === envId) return;
		this.stop();
		this.envId = envId;
		const id = envId;
		this.handle = openStream<RunsList>({
			path: `/v1/environments/${envId}/runs/stream`,
			events: 'runs',
			onEvent: (data) => {
				if (this.envId !== id) return;
				this.runs = data.runs;
			}
		});
	}

	stop(): void {
		this.handle?.close();
		this.handle = null;
		this.envId = null;
		this.runs = null;
	}

	/**
	 * The live runs of envId, newest first. Null until the first document
	 * arrives, or while the store follows another environment.
	 */
	of(envId: string | null | undefined): Run[] | null {
		return envId && envId === this.envId ? this.runs : null;
	}

	/** The run the followed environment is working on now, if any. */
	get active(): Run | null {
		return this.runs ? activeRun(this.runs) : null;
	}
}

export const envRuns = new EnvRunsStore();
