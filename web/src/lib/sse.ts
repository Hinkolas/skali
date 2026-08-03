// Browser-side SSE consumption over the /api/stream proxy. EventSource
// handles reconnection and Last-Event-ID on its own for transient drops, but
// gives up permanently on HTTP errors (e.g. a daemon restart returning 502),
// so this wrapper re-creates the source with capped backoff and replays the
// last seen event id as ?after= (a fresh EventSource sends no header).

export type StreamState = 'connecting' | 'open' | 'retrying' | 'closed';

export interface StreamHandle {
	close(): void;
}

export interface StreamOptions<T> {
	/** API path, e.g. `/v1/environments/{id}/status/stream`. */
	path: string;
	/** Event name(s) to listen for, e.g. 'status' or ['run']. */
	events: string | string[];
	/** Initial resume cursor, appended as ?after= (step logs only). */
	after?: string;
	onEvent: (data: T, id: string | null, event: string) => void;
	onState?: (state: StreamState) => void;
}

const MAX_BACKOFF_MS = 15_000;

export function openStream<T>(options: StreamOptions<T>): StreamHandle {
	const names = Array.isArray(options.events) ? options.events : [options.events];
	let source: EventSource | null = null;
	let closed = false;
	let retryTimer: ReturnType<typeof setTimeout> | null = null;
	let backoff = 1000;
	let lastID: string | null = null;

	const setState = (state: StreamState) => options.onState?.(state);

	const connect = () => {
		if (closed) return;
		setState('connecting');
		const cursor = lastID ?? options.after;
		const query = cursor ? `?after=${encodeURIComponent(cursor)}` : '';
		source = new EventSource(`/api/stream${options.path}${query}`);
		source.onopen = () => {
			backoff = 1000;
			setState('open');
		};
		for (const name of names) {
			source.addEventListener(name, (event: MessageEvent) => {
				if (event.lastEventId) lastID = event.lastEventId;
				let data: T;
				try {
					data = JSON.parse(event.data) as T;
				} catch {
					return;
				}
				options.onEvent(data, event.lastEventId || null, name);
			});
		}
		source.onerror = () => {
			// EventSource retries transient drops itself (readyState
			// CONNECTING); only a permanently closed source needs the manual
			// backoff re-create.
			if (closed || source?.readyState !== EventSource.CLOSED) {
				if (!closed) setState('retrying');
				return;
			}
			source?.close();
			source = null;
			setState('retrying');
			retryTimer = setTimeout(connect, backoff);
			backoff = Math.min(backoff * 2, MAX_BACKOFF_MS);
		};
	};

	connect();

	return {
		close() {
			closed = true;
			if (retryTimer) clearTimeout(retryTimer);
			source?.close();
			source = null;
			setState('closed');
		}
	};
}
