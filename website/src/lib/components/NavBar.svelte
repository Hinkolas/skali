<script lang="ts">
	import { resolve } from '$app/paths';
	import { links } from '$lib/links';
	import GitHubIcon from './GitHubIcon.svelte';
	import LogoMark from './LogoMark.svelte';

	let { version }: { version: string } = $props();
</script>

<header class="relative z-10">
	<nav aria-label="Main" class="mx-auto flex h-18 max-w-300 items-center gap-10 px-6 text-sm">
		<a href={resolve('/')} class="flex items-center gap-2.5 text-text-primary">
			<LogoMark />
			<span class="text-[17px] font-semibold tracking-[-0.02em]">skali</span>
		</a>
		<div class="hidden gap-7 sm:flex">
			<a
				href={links.docs}
				rel="external"
				class="text-text-muted transition-colors hover:text-text-primary">Docs</a
			>
			<a
				href={links.releases}
				rel="external"
				class="text-text-muted transition-colors hover:text-text-primary"
			>
				Changelog
			</a>
		</div>
		<div class="ml-auto flex items-center gap-2">
			<a
				href={links.releases}
				rel="external"
				class="hidden font-mono text-xs text-text-faint transition-colors hover:text-text-secondary sm:block"
			>
				{version}
			</a>
			<a
				href={links.repo}
				rel="external"
				aria-label="skali on GitHub"
				class="flex size-11 items-center justify-center text-text-muted transition-colors hover:text-text-primary"
			>
				<GitHubIcon />
			</a>
		</div>
	</nav>
</header>
