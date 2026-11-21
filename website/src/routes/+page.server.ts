import { currentRelease } from '#lib/server/release.ts';

export const load = () => ({ release: currentRelease() });
