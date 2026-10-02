<script lang="ts">
	import { resolve } from '$app/paths';
	import LogoMark from '$lib/components/LogoMark.svelte';
	import { links } from '$lib/links';

	const columns = [
		{
			title: 'Product',
			links: [
				{ label: 'Changelog', href: links.releases },
				{ label: 'Roadmap', href: links.roadmap }
			]
		},
		{
			title: 'Docs',
			links: [
				{ label: 'Getting started', href: links.gettingStarted },
				{ label: 'The manifest', href: links.manifest },
				{ label: 'Documentation', href: links.docs },
				{ label: 'Known limitations', href: links.limitations }
			]
		},
		{
			title: 'Project',
			links: [
				{ label: 'GitHub', href: links.repo },
				{ label: 'Security', href: links.security },
				{ label: 'License', href: links.license }
			]
		}
	];
</script>

<footer class="border-t border-border-subtle">
	<div class="mx-auto flex max-w-300 flex-col gap-14 px-6 pt-16 pb-10">
		<div class="grid grid-cols-[repeat(auto-fit,minmax(min(180px,100%),1fr))] gap-8">
			<div class="flex flex-col gap-3">
				<div class="flex items-center gap-2.5">
					<LogoMark size={24} />
					<span class="text-base font-semibold tracking-[-0.02em]">skali</span>
				</div>
				<p class="m-0 max-w-55 text-[13px] leading-[1.6] text-text-faint">
					A self-hosted application platform for your own servers.
				</p>
			</div>
			{#each columns as column (column.title)}
				<nav aria-label={column.title} class="flex flex-col gap-3 text-sm">
					<span class="font-mono text-[11px] tracking-[0.08em] text-text-faint uppercase">
						{column.title}
					</span>
					{#each column.links as link (link.label)}
						<a
							href={link.href}
							rel="external"
							class="text-text-muted transition-colors hover:text-text-primary"
						>
							{link.label}
						</a>
					{/each}
				</nav>
			{/each}
		</div>
		<div
			class="flex flex-wrap justify-between gap-3 border-t border-border-subtle pt-6 text-[13px] text-text-faint"
		>
			<span>© {new Date().getFullYear()} skali. Apache License 2.0.</span>
			<div class="flex flex-wrap gap-6">
				<a href={resolve('/imprint')} class="transition-colors hover:text-text-primary">Imprint</a>
				<a href={links.repo} rel="external" class="transition-colors hover:text-text-primary"
					>github.com/Hinkolas/skali</a
				>
			</div>
		</div>
	</div>
</footer>
