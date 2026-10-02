<script lang="ts">
	/**
	 * A static rendering of the Studio's project overview at 1280×820, built
	 * from the real shell, stat tiles, storage bar and service cards. It is a
	 * picture, not UI: the caller crops it and hides it from assistive tech.
	 */
	import Activity from '@lucide/svelte/icons/activity';
	import Archive from '@lucide/svelte/icons/archive';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import LayoutDashboard from '@lucide/svelte/icons/layout-dashboard';
	import Plus from '@lucide/svelte/icons/plus';
	import ScrollText from '@lucide/svelte/icons/scroll-text';
	import Search from '@lucide/svelte/icons/search';
	import Settings2 from '@lucide/svelte/icons/settings-2';
	import LogoMark from '$lib/components/LogoMark.svelte';
	import ServiceTile, { type Kind } from './ServiceTile.svelte';

	const nav = [
		{ label: 'Overview', icon: LayoutDashboard, active: true },
		{ label: 'Activity', icon: Activity },
		{ label: 'Logs', icon: ScrollText },
		{ label: 'Backups', icon: Archive },
		{ label: 'Settings', icon: Settings2 }
	];

	const stats = [
		{
			label: 'Requests',
			value: '1.2k',
			unit: '/min',
			line: 'M0 20 L14 18 L28 21 L42 15 L56 17 L70 12 L84 14 L98 9 L112 13 L126 10 L140 6 L154 9 L168 5 L182 8 L200 4',
			meta: [
				['total', '1.4M / 24h'],
				['peak', '2.1k /min']
			]
		},
		{
			label: 'CPU',
			value: '0.4',
			unit: '/ 2 cores',
			line: 'M0 16 L14 17 L28 14 L42 18 L56 15 L70 16 L84 9 L98 14 L112 16 L126 13 L140 15 L154 11 L168 14 L182 12 L200 14',
			meta: [
				['avg', '0.3'],
				['peak', '0.9']
			]
		},
		{
			label: 'Memory',
			value: '412',
			unit: 'MiB / 1.5 GiB',
			line: 'M0 14 L14 13 L28 13 L42 12 L56 12 L70 11 L84 12 L98 11 L112 10 L126 11 L140 10 L154 10 L168 9 L182 10 L200 9',
			meta: [
				['avg', '380 MiB'],
				['peak', '455 MiB']
			]
		},
		{
			label: 'Traffic',
			value: '3.8',
			unit: 'GiB / 24h',
			line: 'M0 22 L14 20 L28 16 L42 12 L56 10 L70 13 L84 18 L98 21 L112 19 L126 14 L140 9 L154 7 L168 11 L182 16 L200 19',
			meta: [
				['in', '412 MiB'],
				['out', '3.4 GiB']
			]
		}
	];

	const storage = [
		{ label: 'Volumes 2.0 GiB', width: '2.5%', color: 'bg-service-app' },
		{ label: 'Databases 6.4 GiB', width: '8%', color: 'bg-service-db' },
		{ label: 'Buckets 18 GiB', width: '22.5%', color: 'bg-service-storage' }
	];

	const services: {
		name: string;
		kind: Kind;
		label: string;
		source: string;
		meta: [string, string][];
		focused?: boolean;
	}[] = [
		{
			name: 'web',
			kind: 'application',
			label: 'application · build',
			source: './web',
			meta: [
				['replicas', '3/3'],
				['ports', '1'],
				['', 'routed']
			],
			focused: true
		},
		{
			name: 'worker',
			kind: 'application',
			label: 'application · image',
			source: 'ghcr.io/acme/worker:1.8.2',
			meta: [
				['replicas', '2/2'],
				['ports', '0']
			]
		},
		{
			name: 'db',
			kind: 'database',
			label: 'database · postgres 17',
			source: '10 GiB',
			meta: [
				['isolation', 'project'],
				['tier', 'asynchronous']
			]
		},
		{
			name: 'uploads',
			kind: 'bucket',
			label: 'bucket · private',
			source: '50 GiB quota',
			meta: [
				['visibility', 'private'],
				['versioning', 'enabled']
			]
		}
	];
</script>

<div
	class="flex h-205 w-320 flex-col overflow-hidden rounded-[14px] bg-surface-base leading-normal text-text-primary"
	style="background-image: radial-gradient(990px 660px at 30px 31px, rgb(124 92 255 / 0.09) 0%, rgb(124 92 255 / 0.055) 20%, rgb(124 92 255 / 0.029) 40%, rgb(124 92 255 / 0.012) 60%, rgb(124 92 255 / 0.003) 80%, transparent 100%)"
>
	<!-- logo + topbar -->
	<div class="flex h-[62px] shrink-0 gap-[11px] px-[11px]">
		<div class="flex w-[275px] shrink-0 items-center gap-[11px] px-[4.4px]">
			<LogoMark size={28.6} />
			<span class="text-[17px] font-semibold tracking-[-0.01em]">skali</span>
		</div>
		<div class="flex grow items-center gap-[13.2px]">
			<div class="-ml-[8.8px] flex items-center gap-[4.4px] text-[14px] font-medium">
				<span class="px-[8.8px] py-[4.4px] text-text-tertiary">acme</span>
				<span class="text-[13px] text-text-ghost">/</span>
				<span class="flex items-center gap-[4.4px] px-[8.8px] py-[4.4px]">
					Storefront <ChevronDown size={13} class="text-text-ghost" />
				</span>
				<span class="text-[13px] text-text-ghost">/</span>
				<span
					class="flex items-center gap-[4.4px] px-[8.8px] py-[4.4px] font-mono text-[13px] text-text-secondary"
				>
					production <ChevronDown size={13} class="text-text-ghost" />
				</span>
			</div>
			<span class="grow"></span>
			<span
				class="flex h-[35.2px] items-center gap-[8.8px] rounded-[11px] border border-border-strong bg-white/2 px-[15.4px] text-[14px] font-medium text-text-secondary"
			>
				Actions <ChevronDown size={13} class="text-text-ghost" />
			</span>
			<span
				class="flex h-[35.2px] w-[193.6px] items-center justify-between rounded-[11px] border border-border-strong bg-white/2 pr-3 pl-[15.4px]"
			>
				<span class="flex items-center gap-[8.8px] text-[14px] text-text-muted">
					<Search size={14} class="text-text-ghost" /> Search
				</span>
				<span
					class="rounded-md border border-border-strong px-[5.5px] py-px font-mono text-[11px] text-text-faint"
				>
					⌘K
				</span>
			</span>
			<span class="flex items-center gap-[4.4px] p-[2.2px]">
				<span
					class="flex size-[30.8px] items-center justify-center rounded-full bg-linear-135 from-[#37324e] to-[#232030] text-[12px] font-semibold text-accent-nav"
				>
					NH
				</span>
				<ChevronDown size={14} class="text-text-ghost" />
			</span>
		</div>
	</div>

	<div class="flex min-h-0 grow gap-[11px] px-[11px] pb-[11px]">
		<!-- sidebar -->
		<div class="flex w-[275px] shrink-0 flex-col">
			<div
				class="px-[4.4px] pt-2 pb-[8.8px] text-[11px] font-semibold tracking-[0.13em] text-text-ghost"
			>
				PROJECT
			</div>
			<div class="flex flex-col gap-[2.2px] px-[4.4px]">
				{#each nav as item (item.label)}
					<span
						class={[
							'flex items-center gap-[12.1px] rounded-[11px] px-[13.2px] py-[8.8px] text-[15px]',
							item.active
								? 'bg-accent/10 font-medium text-accent-nav inset-ring inset-ring-accent/25'
								: 'text-text-tertiary'
						]}
					>
						<item.icon size={17} strokeWidth={1.75} class="opacity-90" />
						{item.label}
					</span>
				{/each}
			</div>
			<div
				class="flex items-center px-[4.4px] pt-[19.8px] pb-[8.8px] text-[11px] font-semibold tracking-[0.13em] text-text-ghost"
			>
				SERVICES
				<span class="ml-[6.6px] font-mono font-normal tracking-normal text-accent">4</span>
			</div>
			<div class="flex flex-col gap-[2.2px] px-[4.4px]">
				{#each services as service (service.name)}
					<span
						class="flex items-center gap-[11px] rounded-[11px] px-[13.2px] py-[7.7px] text-[15px] text-text-secondary"
					>
						<ServiceTile kind={service.kind} />
						{service.name}
						<span class="ml-auto size-2 rounded-full bg-status-success"></span>
					</span>
				{/each}
				<span
					class="mt-[6.6px] flex items-center gap-[8.8px] rounded-[11px] border border-dashed border-border-strong px-[13.2px] py-[8.8px] text-[15px] text-text-faint opacity-60"
				>
					<Plus size={15} /> New service
				</span>
			</div>
			<span class="grow"></span>
			<div
				class="flex items-center gap-[7.7px] px-[4.4px] pb-[11px] font-mono text-[11px] text-text-faint"
			>
				<span class="size-[6.6px] rounded-full bg-status-success"></span>
				3/3 nodes online
			</div>
		</div>

		<!-- main surface -->
		<div
			class="flex min-w-0 grow flex-col overflow-hidden rounded-2xl border border-border-default bg-surface-raised px-[26px] py-[24.2px]"
		>
			<div class="mb-[24.2px]">
				<div class="text-[29px] leading-[1.2] font-semibold tracking-[-0.02em]">Storefront</div>
				<div class="mt-[6.6px] flex items-center gap-[8.8px] text-[14px] text-text-muted">
					<span class="size-2 rounded-full bg-status-success"></span>
					4 services · 4/4 healthy · active
				</div>
			</div>

			<div class="mb-[28.6px] grid grid-cols-4 gap-[15.4px]">
				{#each stats as stat (stat.label)}
					<div
						class="rounded-[15px] border border-border-raised bg-surface-card px-[19.8px] py-[17.6px]"
					>
						<div
							class="mb-[13.2px] text-[11px] font-semibold tracking-[0.12em] text-text-ghost uppercase"
						>
							{stat.label}
						</div>
						<div class="text-[26px] leading-[1.1] font-semibold tracking-[-0.02em]">
							{stat.value}<span class="ml-[4.4px] text-[14px] font-medium text-text-muted"
								>{stat.unit}</span
							>
						</div>
						<svg
							class="mt-[13.2px] block h-7 w-full"
							viewBox="0 0 200 28"
							preserveAspectRatio="none"
						>
							<path d="{stat.line} L200 28 L0 28 Z" fill="rgb(124 92 255 / 0.14)" />
							<path
								d={stat.line}
								fill="none"
								stroke="#7c5cff"
								stroke-width="1.5"
								stroke-linecap="round"
								stroke-linejoin="round"
								vector-effect="non-scaling-stroke"
							/>
						</svg>
						<div
							class="mt-[11px] flex flex-wrap gap-x-[13.2px] gap-y-[4.4px] font-mono text-[11px] text-text-muted"
						>
							{#each stat.meta as [key, value] (key)}
								<span class="flex items-center gap-[6.6px]">
									{#if stat.label === 'Traffic'}
										<span class={['size-2 rounded-full', key === 'in' ? 'bg-accent' : 'bg-chart-2']}
										></span>
									{/if}
									{key} <span class="text-text-primary">{value}</span>
								</span>
							{/each}
						</div>
					</div>
				{/each}
			</div>

			<div class="mb-[15.4px] flex items-baseline gap-[11px]">
				<span class="text-[17px] font-semibold">Storage</span>
				<span class="text-[13px] text-text-muted">env production</span>
			</div>
			<div
				class="mb-[28.6px] flex items-center gap-[17.6px] rounded-[15px] border border-border-raised bg-surface-card px-[19.8px] py-[17.6px]"
			>
				<div class="grow">
					<div class="flex h-[11px] gap-px overflow-hidden rounded-full bg-white/6">
						{#each storage as segment (segment.label)}
							<span class={segment.color} style:width={segment.width}></span>
						{/each}
					</div>
					<div class="mt-[11px] flex gap-[17.6px] font-mono text-[11px] text-text-faint">
						{#each storage as segment (segment.label)}
							<span class="flex items-center gap-[6.6px]">
								<span class="size-2 rounded-full {segment.color}"></span>{segment.label}
							</span>
						{/each}
						<span class="ml-auto text-text-muted">26 GiB of 80 GiB</span>
					</div>
				</div>
				<span class="flex items-center gap-1.5 text-[13px] text-text-muted">
					3 services <ChevronDown size={16} class="text-text-faint" />
				</span>
			</div>

			<div class="mb-[15.4px] flex items-baseline gap-[11px]">
				<span class="text-[17px] font-semibold">Services</span>
				<span class="text-[13px] text-text-muted">env production</span>
			</div>
			<div class="grid grid-cols-3 gap-[15.4px]">
				{#each services as service (service.name)}
					<div
						class={[
							'flex flex-col gap-[13.2px] rounded-[15px] border bg-surface-card px-[19.8px] py-[17.6px]',
							service.focused ? 'border-accent/35' : 'border-border-raised'
						]}
					>
						<div class="flex items-center gap-[11px]">
							<ServiceTile kind={service.kind} size={33} />
							<span class="flex flex-col leading-[1.3]">
								<span class="text-[15px] font-semibold">{service.name}</span>
								<span class="font-mono text-[11px] text-text-faint">{service.label}</span>
							</span>
							<span class="ml-auto flex items-center gap-[6.6px] text-[13px] text-status-success">
								<span class="size-2 rounded-full bg-status-success"></span>Healthy
							</span>
						</div>
						<div class="font-mono text-[12px] text-text-muted">{service.source}</div>
						<div
							class="flex gap-[13.2px] border-t border-border-subtle pt-[12.1px] font-mono text-[11px] text-text-faint"
						>
							{#each service.meta as [key, value] (value)}
								<span>{key} <span class="text-text-secondary">{value}</span></span>
							{/each}
						</div>
					</div>
				{/each}
				<div
					class="flex min-h-[132px] items-center justify-center gap-[6.6px] rounded-[15px] border border-dashed border-border-strong text-[14px] text-text-faint opacity-60"
				>
					<Plus size={15} /> Add a service
				</div>
			</div>
		</div>
	</div>
</div>
