// deferred() turns a promise a load returned unawaited into reactive state
// a page can render around: `value` is the last resolved result, `pending`
// says whether a newer one is on its way, `error` carries a rejection.
//
// SvelteKit does not await promises a universal load returns at the top
// level, so a page that returns `{ summary: fetchSummary() }` paints as
// soon as the shell is ready and fills in when the promise settles. On
// invalidateAll() the load runs again and hands over a fresh promise; the
// previous value stays on screen while it is pending (the same rule the
// metrics poller follows), so a reload never flashes back to a skeleton.
// Show a Skeleton only while `pending && value === null`.
//
// Call it during component initialisation: it owns an $effect.

export interface Deferred<T> {
	readonly value: T | null;
	readonly pending: boolean;
	readonly error: unknown;
}

export function deferred<T>(source: () => Promise<T> | T): Deferred<T> {
	let value = $state<T | null>(null);
	let pending = $state(true);
	let error = $state<unknown>(null);

	$effect(() => {
		const next = source();
		if (!(next instanceof Promise)) {
			value = next;
			pending = false;
			error = null;
			return;
		}
		// A newer promise supersedes this one: its settlement is dropped so
		// an older, slower reload never overwrites a fresher result.
		let superseded = false;
		pending = true;
		next.then(
			(resolved) => {
				if (superseded) return;
				value = resolved;
				error = null;
				pending = false;
			},
			(rejected: unknown) => {
				if (superseded) return;
				error = rejected;
				pending = false;
			}
		);
		return () => {
			superseded = true;
		};
	});

	return {
		get value() {
			return value;
		},
		get pending() {
			return pending;
		},
		get error() {
			return error;
		}
	};
}
