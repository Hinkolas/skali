import { expect, test } from 'vitest';
import { hasStatefulServices } from '../src/lib/models/service';
import type { ProjectDefinition } from '../src/lib/types/definition';

// The backup guards (the manual button's refusal and the idle mark on a
// schedule) both hang on this one question.
const definition = (parts: Partial<ProjectDefinition>): ProjectDefinition =>
	({ version: 1, ...parts }) as unknown as ProjectDefinition;

test('a missing definition is treated as stateful so nothing is refused blindly', () => {
	expect(hasStatefulServices(null)).toBe(true);
});

test('only applications without volumes leave nothing to back up', () => {
	expect(hasStatefulServices(definition({}))).toBe(false);
	expect(
		hasStatefulServices(
			definition({ applications: { web: {} } } as unknown as Partial<ProjectDefinition>)
		)
	).toBe(false);
});

test('a database, a bucket, or an application volume makes the project stateful', () => {
	expect(
		hasStatefulServices(
			definition({ databases: { db: {} } } as unknown as Partial<ProjectDefinition>)
		)
	).toBe(true);
	expect(
		hasStatefulServices(
			definition({ buckets: { media: {} } } as unknown as Partial<ProjectDefinition>)
		)
	).toBe(true);
	expect(
		hasStatefulServices(
			definition({
				applications: { web: { volumes: { data: {} } } }
			} as unknown as Partial<ProjectDefinition>)
		)
	).toBe(true);
});
