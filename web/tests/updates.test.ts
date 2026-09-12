import { expect, test } from 'vitest';
import {
	commonVersion,
	tallyVersions,
	updatePresentation,
	updateSummary,
	type UpdateStatus
} from '../src/lib/types/updates';

test('incomplete and unknown updates cannot appear up to date', () => {
	for (const state of [
		'unknown',
		'not_checked',
		'no_release',
		'incomplete',
		'failed',
		'updating'
	] as const) {
		const status = {
			summary: {
				state,
				target_version: 'v0.1.0-alpha.4',
				action: state === 'incomplete' ? 'finish' : ''
			}
		} as UpdateStatus;
		const presentation = updatePresentation(status);
		expect(presentation.title).not.toBe('You are up to date');
		expect(presentation.tone).not.toBe('success');
		if (state === 'incomplete') expect(presentation.action).toBe('Finish update');
	}
});

test('an old daemon response remains renderable during a rolling update', () => {
	const summary = updateSummary({ installed: { version: 'v0.1.0-alpha.4' } } as UpdateStatus);
	expect(summary.state).toBe('unknown');
	expect(summary.action).toBe('');
	expect(summary.progress.percent).toBe(0);
	expect(summary.detail).toContain('rollout');
});

test('only a verified current cluster has a success label', () => {
	expect(
		updatePresentation({ summary: { state: 'current', action: '' } } as UpdateStatus)
	).toMatchObject({ title: 'You are up to date', tone: 'success' });
	expect(updatePresentation({} as UpdateStatus).tone).toBe('neutral');
});

test('tallyVersions names one version or spells out a split', () => {
	expect(tallyVersions(['v1', 'v1', 'v1'])).toBe('v1');
	expect(tallyVersions(['v2', 'v1', 'v2'])).toBe('2 on v2 · 1 on v1');
	expect(tallyVersions(['v1', undefined])).toBe('v1 · 1 not reported');
	expect(tallyVersions([undefined])).toBe('not reported');
});

test('commonVersion picks the most reported version', () => {
	expect(commonVersion(['v1', 'v2', 'v2'])).toBe('v2');
	expect(commonVersion([undefined])).toBeUndefined();
});
