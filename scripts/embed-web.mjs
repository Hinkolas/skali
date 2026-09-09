import { readFileSync, statSync, readdirSync, rmSync, mkdirSync, cpSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { resolve, join } from 'node:path';

const root = fileURLToPath(new URL('../', import.meta.url));
const source = resolve(root, 'web/build');
const destination = resolve(root, 'internal/webui/dist');
const html = readFileSync(join(source, '200.html'), 'utf8');
const assets = [...new Set(html.match(/_app\/[a-zA-Z0-9_./-]+/g) ?? [])];
if (!html.includes('<html') || assets.length === 0) throw new Error('The static console fallback is missing its application assets');
for (const asset of assets) {
    if (!statSync(join(source, asset)).isFile()) throw new Error(`Missing console asset: ${asset}`);
}
mkdirSync(destination, { recursive: true });
for (const name of readdirSync(destination)) {
    if (name !== '.gitkeep') rmSync(join(destination, name), { recursive: true, force: true });
}
cpSync(source, destination, { recursive: true });
writeFileSync(join(destination, '.gitkeep'), '');
console.log(`Embedded static console (${assets.length} entry assets verified)`);
