<script lang="ts">
	// The one text-input style (previously copy-pasted into every modal).
	let {
		value = $bindable(''),
		type = 'text',
		placeholder = '',
		name,
		autocomplete,
		disabled = false,
		mono = false,
		invalid = false,
		class: className = '',
		oninput,
		onkeydown
	}: {
		value?: string;
		type?: 'text' | 'password' | 'email';
		placeholder?: string;
		name?: string;
		autocomplete?: 'off' | 'on' | 'current-password' | 'new-password' | 'email' | 'one-time-code';
		disabled?: boolean;
		mono?: boolean;
		invalid?: boolean;
		class?: string;
		oninput?: (e: Event) => void;
		onkeydown?: (e: KeyboardEvent) => void;
	} = $props();
</script>

<input
	bind:value
	{type}
	{placeholder}
	{name}
	{autocomplete}
	{disabled}
	{oninput}
	{onkeydown}
	class="bg-surface-input text-text-primary w-full rounded-[11px] border px-3.25 py-2.75 text-lg transition-colors focus:outline-none disabled:opacity-60 {invalid
		? 'border-status-danger/60 focus:border-status-danger'
		: 'border-border-strong focus:border-accent/50'} {mono ? 'font-mono text-md' : ''} {className}"
/>
