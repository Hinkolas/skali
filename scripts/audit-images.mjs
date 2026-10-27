#!/usr/bin/env node
// Advisory inventory, not an exploitability verdict. No findings are hidden.
// Usage: TRIVY_BIN=trivy node scripts/audit-images.mjs [output-directory]
import { execFileSync, spawn } from 'node:child_process';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';

process.chdir(fileURLToPath(new URL('../', import.meta.url)));
const output = resolve(process.argv[2] ?? '.cache/dependency-audit/images');
mkdirSync(output, { recursive: true });
const trivy = process.env.TRIVY_BIN || 'trivy';
const platforms = (process.env.AUDIT_PLATFORMS || 'linux/amd64,linux/arm64').split(',');
const inventory = JSON.parse(execFileSync('go', ['run', 'scripts/audit-images.go'], { encoding: 'utf8' }));
// Optional candidate images augment the inventory; unpublished local images
// require AUDIT_IMAGE_SOURCE=docker instead of the default remote registry.
for (const ref of (process.env.AUDIT_EXTRA_IMAGES || '').split(',').filter(Boolean)) {
	inventory.push({ ref, scopes: ['candidate'], source: 'AUDIT_EXTRA_IMAGES' });
}
const scopes = (process.env.AUDIT_SCOPES || 'runtime,build,examples,candidate').split(',');
const selected = inventory.filter((image) => image.scopes.some((scope) => scopes.includes(scope)));
if (!selected.length || platforms.some((platform) => !/^linux\/(amd64|arm64)$/.test(platform))) {
	throw new Error('Select at least one inventory scope and linux/amd64 and/or linux/arm64');
}
writeFileSync(join(output, 'inventory.json'), JSON.stringify(inventory, null, 2) + '\n');
writeFileSync(join(output, 'trivy-version.txt'), execFileSync(trivy, ['--version']));
writeFileSync(join(output, 'commit.txt'), execFileSync('git', ['rev-parse', 'HEAD']));
writeFileSync(join(output, 'working-tree.txt'), execFileSync('git', ['status', '--short']));

function run(args, logfile) {
	return new Promise((resolve, reject) => {
		const child = spawn(trivy, args, { stdio: ['ignore', 'pipe', 'pipe'] });
		const logs = [];
		child.stdout.on('data', (chunk) => logs.push(chunk));
		child.stderr.on('data', (chunk) => logs.push(chunk));
		child.on('error', reject);
		child.on('close', (code) => {
			writeFileSync(logfile, Buffer.concat(logs));
			resolve(code);
		});
	});
}

// Update once. Workers use an in-memory analysis cache so parallel scans do
// not contend for Trivy's filesystem-cache lock; the advisory DB is shared.
const cache = resolve(process.env.TRIVY_CACHE_DIR || join(output, 'cache'));
if (await run(['image', '--cache-dir', cache, '--download-db-only'], join(output, 'database.log')) !== 0) {
	throw new Error(`Trivy database update failed: ${join(output, 'database.log')}`);
}
writeFileSync(join(output, 'database.json'), readFileSync(join(cache, 'db', 'metadata.json')));
const queue = selected.flatMap((image, i) => platforms.map((platform) => ({ image, platform, id: `${i}-${platform.replaceAll('/', '-')}` })));
const results = [];
async function worker() {
	while (queue.length) {
		const { image, platform, id } = queue.shift();
		const report = join(output, `${id}.json`);
		const code = await run([
			'image', '--cache-dir', cache, '--cache-backend', 'memory', '--skip-db-update',
			'--scanners', 'vuln', '--image-src', process.env.AUDIT_IMAGE_SOURCE || 'remote',
			'--platform', platform, '--timeout', '10m', '--format', 'json', '--output', report,
			'--config', '', '--ignorefile', '', '--exit-code', '0', image.ref,
		], join(output, `${id}.log`));
		const result = { ...image, platform, report: `${id}.json`, status: 'scan failed', code };
		if (code === 0) {
			const data = JSON.parse(readFileSync(report, 'utf8'));
			result.status = 'scanned';
			result.actualPlatform = `${data.Metadata?.ImageConfig?.os}/${data.Metadata?.ImageConfig?.architecture}`;
			if (result.actualPlatform !== platform) result.status = 'platform mismatch';
			result.digests = data.Metadata?.RepoDigests ?? [];
			result.imageID = data.Metadata?.ImageID;
			result.os = data.Metadata?.OS;
			result.counts = {};
			for (const finding of (data.Results ?? []).flatMap((r) => r.Vulnerabilities ?? [])) {
				result.counts[finding.Severity] = (result.counts[finding.Severity] || 0) + 1;
			}
		}
		results.push(result);
		writeFileSync(join(output, 'summary.json'), JSON.stringify(results, null, 2) + '\n');
		console.log(`${platform} ${image.ref}: ${result.status} ${JSON.stringify(result.counts ?? {})}`);
	}
}
await Promise.all([worker(), worker()]);
// A feed finding is a request for review, not proof of a reachable exploit.
// Scanner/registry errors and unsupported platforms also remain visible.
process.exitCode = results.some((r) => r.status !== 'scanned') ? 2
	: results.some((r) => r.counts.HIGH || r.counts.CRITICAL || r.os?.EOSL) ? 1 : 0;
