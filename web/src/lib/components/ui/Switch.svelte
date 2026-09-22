<script lang="ts">
	// The switch visual: a track with a knob, used by Toggle and by the
	// inline switches of the settings modal. Sizes are whole pixels on
	// purpose: on the theme's 0.275rem spacing unit the spacing-scale track
	// is 22px around a 17.6px knob, and the fractional margins plus a drop
	// shadow below the knob made it sit visibly low in the off state (#54).
	let { checked, class: className = '' }: { checked: boolean; class?: string } = $props();
</script>

<span
	aria-hidden="true"
	class="relative inline-block h-[22px] w-[40px] flex-none rounded-full transition-colors {checked
		? 'bg-accent'
		: 'bg-white/12'} {className}"
>
	<span
		class="bg-surface-base absolute top-[2px] left-[2px] size-[18px] rounded-full transition-transform {checked
			? 'translate-x-[18px]'
			: 'translate-x-0'}"
	></span>
</span>
