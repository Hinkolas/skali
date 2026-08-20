<script lang="ts">
	import { fade } from 'svelte/transition';
	import { cubicOut, quintOut } from 'svelte/easing';
	import { modal, type ModalArchetype } from '$lib/stores/modal.svelte';

	// Global host for the component-based modal system. Mounted once in the root
	// layout; renders the modal stack (`modal.open`/`modal.push`). The shell
	// lives here: backdrop, Esc, and the per-archetype anatomy (placement,
	// width, chrome, entrance). `close` is injected into each content
	// component. Lower layers stay mounted but inert, so a confirm or the sudo
	// reauth prompt can layer above a form without destroying its state.

	// Placement: palettes hang near the top like a finder; everything else
	// centers. Width: questions and the checkpoint stay narrow, work gets room.
	function placementFor(archetype: ModalArchetype) {
		return archetype === 'palette' ? 'items-start pt-[12vh]' : 'items-center';
	}

	function panelFor(archetype: ModalArchetype, size: 'md' | 'lg' | undefined) {
		const base = 'flex max-h-[85dvh] w-full flex-col';
		switch (archetype) {
			case 'palette':
				return `${base} max-h-[60vh] ${size === 'lg' ? 'sm:max-w-xl' : 'sm:max-w-md'}`;
			case 'confirm':
			case 'danger':
				return `${base} sm:max-w-[26rem]`;
			case 'checkpoint':
				return `${base} sm:max-w-[24rem]`;
			default:
				return `${base} ${size === 'lg' ? 'sm:max-w-lg' : 'sm:max-w-md'}`;
		}
	}

	// Chrome: the danger tint and the checkpoint's accent edge are the only
	// color the shell spends; everything else stays the shared overlay.
	function chromeFor(archetype: ModalArchetype) {
		switch (archetype) {
			case 'danger':
				return 'border-status-danger/30 shadow-[0_18px_50px_-12px_rgb(0_0_0/0.7),inset_0_1px_0_rgb(224_89_110/0.16)]';
			case 'checkpoint':
				return 'border-accent/25 shadow-[0_18px_50px_-12px_rgb(0_0_0/0.7),inset_0_1px_0_rgb(139_124_246/0.18)]';
			default:
				return 'border-border-strong shadow-2xl';
		}
	}

	// The checkpoint dims the world harder: work pauses at the gate.
	function backdropFor(archetype: ModalArchetype) {
		return archetype === 'checkpoint'
			? 'bg-surface-base/80 backdrop-blur-md'
			: 'bg-surface-base/70 backdrop-blur-sm';
	}

	// One entrance per archetype, exponential ease-out, nothing over 220ms:
	// forms settle, questions pop, the checkpoint drops in, palettes slide
	// down from the top edge they hang from.
	function panelIn(node: Element, { archetype }: { archetype: ModalArchetype }) {
		const spec = {
			form: { y: 6, scale: 0.97, duration: 190 },
			confirm: { y: 4, scale: 0.95, duration: 150 },
			danger: { y: 4, scale: 0.95, duration: 150 },
			checkpoint: { y: -12, scale: 0.98, duration: 220 },
			palette: { y: -16, scale: 1, duration: 190 }
		}[archetype];
		const easing = archetype === 'checkpoint' ? quintOut : cubicOut;
		return {
			duration: spec.duration,
			easing,
			css: (t: number, u: number) =>
				`transform: translateY(${u * spec.y}px) scale(${1 - u * (1 - spec.scale)}); opacity: ${t};`
		};
	}
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && modal.dismiss()} />

{#each modal.stack as m, i (m.id)}
	{@const isTop = i === modal.stack.length - 1}
	{@const archetype = m.options.archetype ?? 'form'}
	{@const Content = m.component}
	<div
		class="fixed inset-0 flex justify-center p-4 {placementFor(archetype)}"
		style="z-index: {50 + i}"
		inert={!isTop}
	>
		<button
			type="button"
			class="absolute inset-0 cursor-default {backdropFor(archetype)}"
			aria-label="Close"
			tabindex="-1"
			onclick={() => isTop && (m.options.closeOnBackdrop ?? true) && modal.dismiss()}
			transition:fade={{ duration: 150 }}
		></button>

		<div
			role={m.options.role ?? 'dialog'}
			aria-modal="true"
			aria-label={m.options.label ?? ''}
			transition:panelIn={{ archetype }}
			class="bg-surface-overlay text-text-primary relative z-10 overflow-hidden rounded-2xl border {chromeFor(
				archetype
			)} {m.options.panelClass ?? panelFor(archetype, m.options.size)}"
		>
			<Content {...m.props} close={(result?: unknown) => modal.close(m.id, result)} />
		</div>
	</div>
{/each}
