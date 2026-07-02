<script lang="ts">
	import { fade, scale } from 'svelte/transition';
	import { modal } from '$lib/stores/modal.svelte';

	// Global host for the component-based modal system. Mounted once in the root
	// layout; renders whatever `modal.open(...)` made current. The shell lives
	// here: backdrop, Esc, centered scale transition. `close` is injected into
	// the content component.

	const opts = $derived(modal.current?.options ?? {});
	const closeOnBackdrop = $derived(opts.closeOnBackdrop ?? true);
	const size = $derived(opts.size ?? 'md');
	const defaultPanel = $derived(
		`flex max-h-[90dvh] w-full flex-col ${size === 'lg' ? 'sm:max-w-lg' : 'sm:max-w-md'}`
	);
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && modal.dismiss()} />

{#if modal.current}
	{@const m = modal.current}
	{@const Content = m.component}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button
			type="button"
			class="bg-surface-base/70 absolute inset-0 cursor-default backdrop-blur-sm"
			aria-label="Close"
			tabindex="-1"
			onclick={() => closeOnBackdrop && modal.dismiss()}
			transition:fade={{ duration: 150 }}
		></button>

		<div
			role={m.options.role ?? 'dialog'}
			aria-modal="true"
			aria-label={m.options.label ?? ''}
			transition:scale={{ duration: 150, start: 0.96 }}
			class="bg-surface-overlay border-border-strong text-text-primary relative z-10 overflow-hidden rounded-2xl border shadow-2xl {m
				.options.panelClass ?? defaultPanel}"
		>
			<Content {...m.props} close={(result?: unknown) => modal.close(m.id, result)} />
		</div>
	</div>
{/if}
