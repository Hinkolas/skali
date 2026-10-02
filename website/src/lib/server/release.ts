// The release the site advertises, read from the repository's tags when the
// page is prerendered. release.yml rebuilds the site after every release, so
// the version and the install command follow it without an edit here.
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { newestRelease } from '../../../scripts/releases.mjs';

const root = fileURLToPath(new URL('../../../../', import.meta.url));

export type Release = {
	/** The newest stable tag, or the newest prerelease while there is none. */
	tag: string;
	/** The command that installs it. */
	install: string;
};

export function currentRelease(): Release {
	const tags = execFileSync('git', ['tag', '--list', 'v*'], { cwd: root, encoding: 'utf8' });
	const release = newestRelease(tags.split('\n'));
	if (!release) throw new Error('No release tag found (shallow clone?). Fetch the tags.');
	// install.sh only picks stable releases by default; until the first one
	// exists the command has to ask for the beta channel or it fails.
	const channel = release.stable ? '' : 'SKALI_CHANNEL=beta ';
	return { tag: release.tag, install: `curl -fsSL https://skali.dev/install.sh | ${channel}sh` };
}
