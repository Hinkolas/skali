import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { openStream } from '../src/lib/sse';

let sources: FakeEventSource[] = [];
class FakeEventSource {
	static CLOSED = 2;
	readyState = 0;
	onopen = () => {};
	onerror = () => {};
	listeners = new Map<string, (event: MessageEvent) => void>();
	close = vi.fn(() => {
		this.readyState = FakeEventSource.CLOSED;
	});
	constructor(public url: string) {
		sources.push(this);
	}
	addEventListener(name: string, handler: (event: MessageEvent) => void) {
		this.listeners.set(name, handler);
	}
}

beforeEach(() => {
	sources = [];
	vi.useFakeTimers();
	vi.stubGlobal('EventSource', FakeEventSource);
});
afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

test('direct Go streams reconnect with their cursor and stop on teardown', () => {
	const event = vi.fn();
	const state = vi.fn();
	const handle = openStream({
		path: '/v1/steps/one/logs/stream',
		events: 'log',
		after: 'start',
		onEvent: event,
		onState: state
	});
	expect(sources[0].url).toBe('/api/v1/steps/one/logs/stream?after=start');
	sources[0].onopen();
	sources[0].listeners.get('log')!({
		data: '{"line":"hello"}',
		lastEventId: 'next cursor'
	} as MessageEvent);
	expect(event).toHaveBeenCalledWith({ line: 'hello' }, 'next cursor', 'log');
	// Transient failures stay with EventSource's native reconnect behavior.
	sources[0].onerror();
	vi.advanceTimersByTime(1000);
	expect(sources).toHaveLength(1);
	// Permanent failure creates a fresh connection with the resume cursor.
	sources[0].readyState = FakeEventSource.CLOSED;
	sources[0].onerror();
	vi.advanceTimersByTime(1000);
	expect(sources).toHaveLength(2);
	expect(sources[1].url).toBe('/api/v1/steps/one/logs/stream?after=next%20cursor');
	handle.close();
	vi.advanceTimersByTime(30000);
	expect(sources).toHaveLength(2);
	expect(sources[1].close).toHaveBeenCalledOnce();
	expect(state).toHaveBeenLastCalledWith('closed');
});

test('teardown cancels a pending reconnect', () => {
	const handle = openStream({ path: '/v1/events/stream', events: 'event', onEvent: vi.fn() });
	sources[0].readyState = FakeEventSource.CLOSED;
	sources[0].onerror();
	handle.close();
	vi.advanceTimersByTime(30000);
	expect(sources).toHaveLength(1);
});
