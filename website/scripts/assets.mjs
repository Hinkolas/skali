// Copies the files the site serves verbatim from the rest of the repository
// into static/ before every dev and build run:
//
//   /install.sh               install.sh from this checkout. The script picks
//                             the release itself, so main is always right.
//   /schemas/v1/<name>.json   the editor schemas from the newest release tag,
//                             not main: main may already describe fields the
//                             released binaries reject. Each file is served at
//                             its own $id.
//
// SCHEMA_REF overrides the tag (any git ref, e.g. SCHEMA_REF=HEAD to preview
// unreleased schema changes). Both outputs are gitignored.
import { execFileSync } from 'node:child_process';
import { copyFileSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { basename, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../../', import.meta.url));
const output = fileURLToPath(new URL('../static/', import.meta.url));
const git = (...args) => execFileSync('git', args, { cwd: root, encoding: 'utf8' });

copyFileSync(join(root, 'install.sh'), join(output, 'install.sh'));

const ref = process.env.SCHEMA_REF || newestRelease(git('tag', '--list', 'v*').split('\n'));
if (!ref) {
	throw new Error('No release tag found (shallow clone?). Fetch tags or set SCHEMA_REF=HEAD.');
}
const schemas = git('ls-tree', '--name-only', ref, 'schemas/')
	.split('\n')
	.filter((path) => path.endsWith('.schema.json'));
if (schemas.length === 0) throw new Error(`${ref} has no schemas/*.schema.json`);

const destination = join(output, 'schemas', 'v1');
rmSync(join(output, 'schemas'), { recursive: true, force: true });
mkdirSync(destination, { recursive: true });
for (const path of schemas) {
	const source = git('show', `${ref}:${path}`);
	const url = `https://skali.dev/schemas/v1/${basename(path)}`;
	const id = JSON.parse(source).$id;
	if (id !== url) throw new Error(`${ref}:${path} has $id ${id}, but would be served at ${url}`);
	writeFileSync(join(destination, basename(path)), source);
}
console.log(`Copied install.sh and ${schemas.length} schemas from ${ref}`);

// newestRelease prefers the newest stable tag and falls back to the newest
// prerelease while there is none, ordered by semver precedence.
function newestRelease(tags) {
	const versions = tags
		.map((tag) => ({ tag, match: /^v(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/.exec(tag) }))
		.filter(({ match }) => match)
		.map(({ tag, match }) => ({
			tag,
			core: match.slice(1, 4).map(Number),
			pre: match[4] ? match[4].split('.') : []
		}));
	const stable = versions.filter((version) => version.pre.length === 0);
	const candidates = stable.length > 0 ? stable : versions;
	return candidates.sort(compare).at(-1)?.tag;
}

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
