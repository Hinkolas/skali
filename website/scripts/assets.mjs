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
import { newestRelease } from './releases.mjs';

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
