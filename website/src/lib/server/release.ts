// The release the site advertises, read from the repository's tags when the
// page is prerendered. release.yml rebuilds the site after every release, so
// the version follows it without an edit here.
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { newestRelease, releaseRef } from '../../../scripts/releases.mjs';

const root = fileURLToPath(new URL('../../../../', import.meta.url));

function tags(): string[] {
	return execFileSync('git', ['tag', '--list', 'v*'], { cwd: root, encoding: 'utf8' }).split('\n');
}

/** The newest stable tag, or the newest prerelease while there is none. */
export function currentRelease(): string {
	const tag = newestRelease(tags());
	if (!tag) throw new Error('No release tag found (shallow clone?). Fetch the tags.');
	return tag;
}

/** The ref release-bound content is read from: RELEASE_REF, else the current release. */
export function contentRef(): string {
	return releaseRef(tags());
}
