// The release the site advertises, read from the repository's tags when the
// page is prerendered. release.yml rebuilds the site after every release, so
// the version follows it without an edit here.
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { newestRelease } from '../../../scripts/releases.mjs';

const root = fileURLToPath(new URL('../../../../', import.meta.url));

/** The newest stable tag, or the newest prerelease while there is none. */
export function currentRelease(): string {
	const tags = execFileSync('git', ['tag', '--list', 'v*'], { cwd: root, encoding: 'utf8' });
	const tag = newestRelease(tags.split('\n'));
	if (!tag) throw new Error('No release tag found (shallow clone?). Fetch the tags.');
	return tag;
}
