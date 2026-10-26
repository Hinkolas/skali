// Copies the files the site serves verbatim from the rest of the repository
// into static/ before every dev and build run:
//
//   /install.sh               install.sh from this checkout. The script picks
//                             the release itself, so main is always right.
//   /schemas/v1/<name>.json   the editor schemas from the newest release tag,
//                             not main: main may already describe fields the
//                             released binaries reject. Each file is served at
//                             its own $id.
//   /schemas/<release>/...   schemas pinned to each available release tag,
//                             including prereleases newer than stable.
//   /schemas/releases.json   the floating selection and available pinned tags.
//
// RELEASE_REF overrides the tag (any git ref, e.g. RELEASE_REF=HEAD to
// preview unreleased schema changes). Both outputs are gitignored.
import { execFileSync } from 'node:child_process';
import { copyFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { releaseRef } from './releases.mjs';
import { publishSchemas } from './schema-assets.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
const output = fileURLToPath(new URL('../static/', import.meta.url));
const git = (...args) => execFileSync('git', args, { cwd: root, encoding: 'utf8' });

copyFileSync(join(root, 'install.sh'), join(output, 'install.sh'));

const tags = git('tag', '--list', 'v*').split('\n');
const ref = releaseRef(tags);
const published = publishSchemas(git, output, ref, tags);
console.log(
	`Copied install.sh, ${published.schemas} floating schemas from ${ref}, and schemas for ${published.releases} releases`
);
