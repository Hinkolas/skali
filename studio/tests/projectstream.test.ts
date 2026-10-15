import { afterEach, beforeEach, expect, test, vi } from 'vitest';

const navigation = vi.hoisted(() => ({
	goto: vi.fn(async () => {}),
	invalidateAll: vi.fn(async () => {})
}));
const toast = vi.hoisted(() => ({ info: vi.fn(), success: vi.fn() }));
vi.mock('$app/navigation', () => navigation);
vi.mock('$app/paths', () => ({ resolve: (path: string) => path }));
vi.mock('$lib/stores/toast.svelte', () => ({ toast }));

import { followProject } from '../src/lib/projectstream';

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
	emit(name: string) {
		this.listeners.get(name)!({ data: '{}', lastEventId: '' } as MessageEvent);
	}
	// An HTTP error answer: EventSource closes for good.
	refuse() {
		this.readyState = FakeEventSource.CLOSED;
		this.onerror();
	}
}

const project = { id: 'p1', name: 'shop', display_name: 'Shop' };
let projectStatus = 200;
const fetchMock = vi.fn(async () =>
	projectStatus === 200
		? Response.json({ id: 'p1' })
		: Response.json(
				{ error: { code: 'not_found', message: 'not found' } },
				{ status: projectStatus }
			)
);

beforeEach(() => {
	sources = [];
	projectStatus = 200;
	vi.clearAllMocks();
	vi.useFakeTimers();
	vi.stubGlobal('EventSource', FakeEventSource);
	vi.stubGlobal('fetch', fetchMock);
});
afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

test('the first ready re-reads what changed before the stream opened', () => {
	const handle = followProject(project);
	sources[0].emit('ready');
	vi.advanceTimersByTime(250);
	expect(navigation.invalidateAll).toHaveBeenCalledOnce();
	handle.close();
});

test('a project that went away while disconnected is left', async () => {
	followProject(project);
	sources[0].emit('ready');
	vi.advanceTimersByTime(250);
	projectStatus = 404;
	sources[0].refuse();
	await vi.waitFor(() => expect(navigation.goto).toHaveBeenCalledWith('/(app)/projects'));
	expect(fetchMock).toHaveBeenCalledWith('/api/v1/projects/p1', expect.anything());
	expect(toast.info).toHaveBeenCalledWith(
		'Project "Shop" was deleted, or you no longer have access to it'
	);
	vi.advanceTimersByTime(30000);
	expect(sources).toHaveLength(1);
});

test('a stream that fails while the project is there keeps retrying', async () => {
	const handle = followProject(project);
	sources[0].refuse();
	await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
	vi.advanceTimersByTime(1000);
	expect(sources).toHaveLength(2);
	expect(navigation.goto).not.toHaveBeenCalled();
	sources[1].emit('ready');
	vi.advanceTimersByTime(250);
	expect(navigation.invalidateAll).toHaveBeenCalledOnce();
	handle.close();
});
