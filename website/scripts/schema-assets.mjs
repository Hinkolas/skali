import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { basename, join } from 'node:path';

/**
 * Publish the floating stable schemas and an immutable URL for each release.
 * Validation content always comes from that tag, never the website checkout.
 * Only the identity and provenance comment change at the versioned URL.
 * @param {(...args: string[]) => string} git
 * @param {string} output
 * @param {string} ref
 * @param {string[]} tags
 */
export function publishSchemas(git, output, ref, tags) {
	const directory = join(output, 'schemas');
	rmSync(directory, { recursive: true, force: true });
	const paths = (tag) =>
		git('ls-tree', '--name-only', tag, 'schemas/')
			.split('\n')
			.filter((path) => path.endsWith('.schema.json'));
	const floating = paths(ref);
	if (floating.length === 0) throw new Error(`${ref} has no schemas/*.schema.json`);
	mkdirSync(join(directory, 'v1'), { recursive: true });
	for (const path of floating) {
		const source = git('show', `${ref}:${path}`);
		const url = `https://skali.dev/schemas/v1/${basename(path)}`;
		const id = JSON.parse(source).$id;
		if (id !== url) throw new Error(`${ref}:${path} has $id ${id}, but would be served at ${url}`);
		writeFileSync(join(directory, 'v1', basename(path)), source);
	}

	const releases = [];
	for (const tag of [...new Set(tags)].sort()) {
		// Only release tags can form a public directory. RELEASE_REF may be an
		// arbitrary local ref, but it affects the floating preview only.
		if (!/^v\d+\.\d+\.\d+(?:-(?:alpha|beta|rc)\.\d+)?$/.test(tag)) continue;
		const schemas = paths(tag);
		if (schemas.length === 0) continue;
		mkdirSync(join(directory, tag), { recursive: true });
		for (const path of schemas) {
			const schema = JSON.parse(git('show', `${tag}:${path}`));
			schema.$id = `https://skali.dev/schemas/${tag}/${basename(path)}`;
			schema.$comment = [schema.$comment, `Schema from Skali ${tag}.`].filter(Boolean).join(' ');
			writeFileSync(join(directory, tag, basename(path)), JSON.stringify(schema, null, 2) + '\n');
		}
		releases.push(tag);
	}
	writeFileSync(
		join(directory, 'releases.json'),
		JSON.stringify({ default: ref, releases }, null, 2) + '\n'
	);
	return { schemas: floating.length, releases: releases.length };
}
