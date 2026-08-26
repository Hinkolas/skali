<script lang="ts">
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import type { RouteStatus, CertificateState } from '$lib/types/status';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import Card from '$lib/components/ui/Card.svelte';

	// Live public routes of one application: domain, balancing policy, and
	// the certificate lifecycle on TLS-capable installations. The panel
	// renders nothing on services without routes.
	let { serviceKey }: { serviceKey: string } = $props();

	const routes = $derived<RouteStatus[]>(
		envStatus.service('application', serviceKey)?.routes ?? []
	);

	const CERT_META: Record<CertificateState, { label: string; dot: string; text: string }> = {
		active: { label: 'cert active', dot: 'bg-status-success', text: 'text-status-success' },
		issuing: { label: 'cert issuing', dot: 'bg-status-warning', text: 'text-status-warning' },
		pending: { label: 'cert pending', dot: 'bg-status-warning', text: 'text-status-warning' },
		failing: { label: 'cert failing', dot: 'bg-status-danger', text: 'text-status-danger' },
		expired: { label: 'cert expired', dot: 'bg-status-danger', text: 'text-status-danger' }
	};

	function url(route: RouteStatus): string {
		const scheme = route.certificate ? 'https' : 'http';
		const path = route.path && route.path !== '/' ? route.path : '';
		return `${scheme}://${route.domain}${path}`;
	}

	function certTitle(route: RouteStatus): string {
		const certificate = route.certificate;
		if (!certificate) return '';
		const parts = [certificate.reason, certificate.message].filter(Boolean);
		if (certificate.not_after) parts.push(`valid until ${certificate.not_after}`);
		return parts.join(' · ');
	}
</script>

{#if routes.length > 0}
	<Card class="p-5">
		<h3 class="text-text-primary mb-3.5 text-xl font-semibold">Routes</h3>
		<div class="flex flex-col gap-2">
			{#each routes as route (route.key)}
				<div class="flex items-center gap-2.5 text-md">
					<!-- eslint-disable svelte/no-navigation-without-resolve -- external URL of the route's own domain -->
					<a
						href={url(route)}
						target="_blank"
						rel="noopener noreferrer"
						class="text-text-primary flex items-center gap-1.5 font-mono hover:underline"
					>
						{url(route)}
						<ExternalLink size={12} />
					</a>
					<!-- eslint-enable svelte/no-navigation-without-resolve -->
					{#if route.strategy === 'least-requests'}
						<span class="text-text-faint font-mono">least-requests</span>
					{/if}
					{#if route.tls === 'optional'}
						<span class="text-text-faint font-mono" title="plain HTTP stays served (tls: optional)"
							>http allowed</span
						>
					{/if}
					{#if route.certificate}
						{@const meta = CERT_META[route.certificate.state]}
						<span class="ml-auto flex items-center gap-1.5 {meta.text}" title={certTitle(route)}>
							<span class="size-[8px] rounded-full {meta.dot}"></span>{meta.label}
						</span>
					{/if}
				</div>
			{/each}
		</div>
	</Card>
{/if}
