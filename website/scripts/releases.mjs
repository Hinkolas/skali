// Release tag selection shared by scripts/assets.mjs and the pages that
// mention the current release.

/**
 * newestRelease prefers the newest stable tag and falls back to the newest
 * prerelease while there is none, ordered by semver precedence.
 *
 * @param {string[]} tags
 * @returns {string | undefined}
 */
export function newestRelease(tags) {
	const versions = tags
		.map((tag) => ({ tag, match: /^v(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/.exec(tag) }))
		.flatMap(({ tag, match }) =>
			match ? [{ tag, core: match.slice(1, 4).map(Number), pre: match[4]?.split('.') ?? [] }] : []
		);
	const stable = versions.filter((version) => version.pre.length === 0);
	const candidates = stable.length > 0 ? stable : versions;
	return candidates.sort(compare).at(-1)?.tag;
}

/**
 * @typedef {{ tag: string, core: number[], pre: string[] }} Version
 * @param {Version} a
 * @param {Version} b
 */
function compare(a, b) {
	for (let i = 0; i < 3; i++) if (a.core[i] !== b.core[i]) return a.core[i] - b.core[i];
	if (a.pre.length === 0 || b.pre.length === 0) return b.pre.length - a.pre.length;
	for (let i = 0; i < Math.min(a.pre.length, b.pre.length); i++) {
		const [x, y] = [a.pre[i], b.pre[i]];
		if (x === y) continue;
		const [nx, ny] = [/^\d+$/.test(x), /^\d+$/.test(y)];
		if (nx && ny) return Number(x) - Number(y);
		if (nx !== ny) return nx ? -1 : 1;
		return x < y ? -1 : 1;
	}
	return a.pre.length - b.pre.length;
}
