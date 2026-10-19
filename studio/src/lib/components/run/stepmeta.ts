// How a step status looks wherever a run's steps are drawn. Class strings
// are literal so Tailwind can see them statically.

import CircleCheck from '@lucide/svelte/icons/circle-check';
import CircleDashed from '@lucide/svelte/icons/circle-dashed';
import CircleMinus from '@lucide/svelte/icons/circle-minus';
import CircleSlash from '@lucide/svelte/icons/circle-slash';
import CircleX from '@lucide/svelte/icons/circle-x';
import Clock from '@lucide/svelte/icons/clock';
import LoaderCircle from '@lucide/svelte/icons/loader-circle';
import type { NavIcon } from '$lib/navigation';
import type { Step, StepStatus } from '$lib/types/runs';

export const STEP_META: Record<
	StepStatus,
	{ icon: NavIcon; class: string; dot: string; spin?: boolean }
> = {
	pending: { icon: CircleDashed, class: 'text-text-ghost', dot: 'bg-white/12' },
	waiting: { icon: Clock, class: 'text-status-warning', dot: 'bg-status-warning/60' },
	running: {
		icon: LoaderCircle,
		class: 'text-status-warning',
		dot: 'bg-status-warning animate-pulse',
		spin: true
	},
	succeeded: { icon: CircleCheck, class: 'text-status-success', dot: 'bg-status-success' },
	failed: { icon: CircleX, class: 'text-status-danger', dot: 'bg-status-danger' },
	skipped: { icon: CircleMinus, class: 'text-text-ghost', dot: 'bg-text-ghost' },
	cancelled: { icon: CircleSlash, class: 'text-text-muted', dot: 'bg-text-muted' }
};

/**
 * A skipped TLS checkpoint is a deferred certificate (the route's domain
 * does not reach this installation yet): a warning, not an omission.
 */
export function deferredTLS(step: Step): boolean {
	return step.key.startsWith('tls:') && step.status === 'skipped';
}
