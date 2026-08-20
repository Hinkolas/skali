<script lang="ts">
	// The one text-input style (previously copy-pasted into every modal).
	let {
		value = $bindable(''),
		type = 'text',
		size = 'md',
		placeholder = '',
		name,
		autocomplete,
		disabled = false,
		mono = false,
		invalid = false,
		autofocus = false,
		class: className = '',
		oninput,
		onkeydown
	}: {
		value?: string;
		type?: 'text' | 'password' | 'email';
		/** md is the form/modal scale; sm sits inside list rows. */
		size?: 'sm' | 'md';
		placeholder?: string;
		name?: string;
		autocomplete?: 'off' | 'on' | 'current-password' | 'new-password' | 'email' | 'one-time-code';
		disabled?: boolean;
		mono?: boolean;
		invalid?: boolean;
		/** Focus on mount; done in an effect so modals can use it too. */
		autofocus?: boolean;
		class?: string;
		oninput?: (e: Event) => void;
		onkeydown?: (e: KeyboardEvent) => void;
	} = $props();

	const sizeClass: Record<string, string> = {
		sm: 'rounded-[9px] px-3 py-1.75',
		md: 'rounded-[11px] px-3.25 py-2.75'
	};

	let el = $state<HTMLInputElement | null>(null);

	$effect(() => {
		if (autofocus) el?.focus();
	});
</script>

<input
	bind:this={el}
	bind:value
	{type}
	{placeholder}
	{name}
	{autocomplete}
	{disabled}
	{oninput}
	{onkeydown}
	class="bg-surface-input text-text-primary w-full border text-lg transition-[border-color,box-shadow] duration-150 focus:outline-none disabled:opacity-60 {sizeClass[
		size
	]} {invalid
		? 'border-status-danger/60 focus:border-status-danger focus:ring-3 focus:ring-status-danger/10'
		: 'border-border-strong focus:border-accent/50 focus:ring-3 focus:ring-accent/10'} {mono
		? 'font-mono text-md'
		: ''} {className}"
/>
