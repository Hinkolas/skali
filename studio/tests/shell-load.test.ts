import { expect, test } from 'vitest';
import { load } from '../src/routes/(app)/+layout';

// The shell load runs before any page paints, so what it asks for is the
// floor of every page load: session, the plain project list, meta, and
// nodes only for instance admins. Never the summary.
function shellFetch(role: 'admin' | 'member') {
	const requested: string[] = [];
	const fetchMock = async (input: RequestInfo | URL) => {
		const path = String(input);
		requested.push(path);
		if (path.endsWith('/api/v1/auth/session')) {
			return Response.json({
				user: {
					id: 'u1',
					email: 'ada@example.com',
					name: 'Ada',
					role,
					create_projects: true,
					two_factor_enabled: false,
					created_at: '2026-01-01T00:00:00Z'
				}
			});
		}
		if (path.endsWith('/api/v1/projects')) {
			return Response.json({
				projects: [
					{ id: 'p1', name: 'shop' },
					{ id: 'p2', name: 'blog' }
				]
			});
		}
		if (path.endsWith('/api/v1/system/meta')) {
			return Response.json({ version: '1.2.3', name: 'Acme' });
		}
		if (path.endsWith('/api/v1/nodes')) {
			return Response.json({
				nodes: [{ name: 'n1', ready: true }],
				observation: { state: 'fresh' }
			});
		}
		return new Response('', { status: 404 });
	};
	return { fetchMock, requested };
}

async function run(role: 'admin' | 'member') {
	const { fetchMock, requested } = shellFetch(role);
	const event = {
		fetch: fetchMock,
		url: new URL('https://skali.test/domains')
	} as unknown as Parameters<typeof load>[0];
	const data = (await load(event)) as Exclude<Awaited<ReturnType<typeof load>>, void>;
	return { data, requested };
}

test('a member gets the shell without summaries or nodes', async () => {
	const { data, requested } = await run('member');
	expect(requested).toEqual(['/api/v1/auth/session', '/api/v1/projects', '/api/v1/system/meta']);
	expect(requested.some((p) => p.includes('include=summary'))).toBe(false);
	expect(data.nodes).toBeNull();
	expect(data.org).toMatchObject({ name: 'Acme', project_count: 2, node_count: null });
	expect(data.projects).toHaveLength(2);
});

test('an instance admin also gets the observed nodes', async () => {
	const { data, requested } = await run('admin');
	expect(requested).toContain('/api/v1/nodes');
	expect(requested.some((p) => p.includes('include=summary'))).toBe(false);
	expect(data.nodes).toHaveLength(1);
	expect(data.org.node_count).toBe(1);
	expect(data.nodesObservation).toEqual({ state: 'fresh' });
});
