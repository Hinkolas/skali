// Per-browser UI preferences (a chosen view, a filter) that outlive the
// tab but never need to reach the server. Storage can be missing or throw
// (private windows, cleared site data), so every access degrades to the
// fallback and the page renders the same either way.

import { browser } from '$app/environment';

export function readPref<T extends string>(key: string, allowed: readonly T[], fallback: T): T {
	if (!browser) return fallback;
	try {
		const value = localStorage.getItem(key);
		return allowed.includes(value as T) ? (value as T) : fallback;
	} catch {
		return fallback;
	}
}

export function writePref(key: string, value: string): void {
	if (!browser) return;
	try {
		localStorage.setItem(key, value);
	} catch {
		// Nothing to do: the choice still applies for this page.
	}
}
