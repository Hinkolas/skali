import { currentRelease } from '$lib/server/release';

export const load = () => ({ release: currentRelease() });
