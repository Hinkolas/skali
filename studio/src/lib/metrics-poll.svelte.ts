import { untrack } from 'svelte';
import type { MetricsWindow } from '$lib/types/metrics';

/**
 * Keeps a windowed metrics document fresh: the route load seeds one window;
 * every other window, and the seeded one once another has been shown, is
 * fetched here and refreshed every interval (the sampler cadence). While a
 * fetch is in flight the seed serves its own window at once and any other
 * window keeps the last series on screen instead of flashing empty.
 *
 * Call during component init (it owns an $effect); read `.metrics`.
 */
export function pollMetrics<T extends { window: MetricsWindow }>(source: {
	seed: () => T | null;
	range: () => MetricsWindow;
	load: (window: MetricsWindow) => Promise<T>;
	/** Skips fetching while false (no environment, undesigned page). */
	enabled?: () => boolean;
	intervalMs?: number;
}): { readonly metrics: T | null } {
	let fetched = $state<T | null>(null);
	const metrics = $derived.by(() => {
		const seed = source.seed();
		const range = source.range();
		if (fetched?.window === range) return fetched;
		if (seed?.window === range) return seed;
		return fetched ?? seed;
	});

	$effect(() => {
		if (source.enabled && !source.enabled()) return;
		const selected = source.range();
		const seed = source.seed();
		// `fetched` is read untracked: a completed fetch must not re-run
		// this effect, or it would fetch again in a loop.
		const seeded = selected === (seed?.window ?? '24h') && untrack(() => fetched) === null;
		let cancelled = false;
		const fetchSeries = async () => {
			try {
				const fresh = await source.load(selected);
				if (!cancelled) fetched = fresh;
			} catch {
				// Keep the last good series; the next tick retries.
			}
		};
		if (!seeded) void fetchSeries();
		const timer = setInterval(fetchSeries, source.intervalMs ?? 30_000);
		return () => {
			cancelled = true;
			clearInterval(timer);
		};
	});

	return {
		get metrics() {
			return metrics;
		}
	};
}
