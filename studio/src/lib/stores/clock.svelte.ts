import { createSubscriber } from 'svelte/reactivity';

// A shared wall clock that ticks once a second while something reactive
// reads it. Live durations ("42s" of a running step) depend on it so they
// keep moving between server events: the run streams re-send a document
// only on change, so a quiet step would otherwise freeze at its first
// rendered value. The interval starts on the first reader and stops with
// the last one, so idle pages pay nothing.
function createClock() {
	let now = Date.now();
	const subscribe = createSubscriber((update) => {
		const timer = setInterval(() => {
			now = Date.now();
			update();
		}, 1000);
		return () => clearInterval(timer);
	});
	return {
		/** Milliseconds since the epoch, at most one second stale. */
		get now() {
			subscribe();
			return now;
		}
	};
}

export const clock = createClock();
