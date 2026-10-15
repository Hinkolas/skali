// Where the site links to outside itself. Documentation the site does not
// render yet points at the repository.
const repo = 'https://github.com/Hinkolas/skali';

export const links = {
	repo,
	releases: `${repo}/releases`,
	docs: `${repo}#documentation`,
	gettingStarted: `${repo}#getting-started`,
	versioning: `${repo}/blob/main/docs/versioning.md`,
	limitations: `${repo}/blob/main/docs/limitations.md`,
	roadmap: `${repo}/blob/main/ROADMAP.md`,
	security: `${repo}/blob/main/SECURITY.md`,
	license: `${repo}/blob/main/LICENSE`,
	/** A repository file as it is at a git ref. */
	file: (ref: string, path: string) => `${repo}/blob/${ref}/${path}`
};
