<script lang="ts">
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import RadioTower from '@lucide/svelte/icons/radio-tower';
	import { page } from '$app/state';
	import type { RouteStatus, CertificateState, RouteProbe } from '$lib/types/status';
	import type { Environment } from '$lib/types/project';
	import { api, ApiError } from '$lib/api/client';
	import { requiredTitle, roleAtLeast } from '$lib/access';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import { formatDateTime } from '$lib/format';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';

	// Live public routes of one application: domain, balancing policy, and
	// the certificate lifecycle on TLS-capable installations. The panel
	// renders nothing on services without routes.
	let { serviceKey }: { serviceKey: string } = $props();

	const routes = $derived<RouteStatus[]>(
		envStatus.service('application', serviceKey)?.routes ?? []
	);

	// A manual probe re-checks every route domain of the environment right
	// now and lets the reconciler act on the result; deploy on the
	// environment, because a domain that arrived triggers an issuance.
	const env = $derived((page.data as { env?: Environment | null }).env ?? null);
	const probeTitle = $derived.by(() => {
		if (!env) return 'no environment selected';
		if (!roleAtLeast(env.access, 'deploy')) return requiredTitle('deploy', 'environment', env.name);
		return 'Check right now whether the domains reach this installation';
	});
	const mayProbe = $derived(env != null && roleAtLeast(env.access, 'deploy'));
	let probing = $state(false);

	async function probe() {
		const target = env;
		if (!target || probing) return;
		probing = true;
		try {
			const res = await api.post<{ routes: RouteProbe[] }>(
				`/v1/environments/${target.id}/routes/probe`
			);
			const mine = res.routes.filter((r) => r.service === serviceKey);
			const arrived = mine.filter((r) => r.edge.state === 'reachable').length;
			if (mine.length === 0) toast.success('No TLS routes to probe');
			else if (arrived === mine.length) toast.success('Every domain reaches this installation');
			else
				toast.warning(
					mine.map((r) => `${r.domain}: ${r.edge.message || r.edge.state}`).join(' · ')
				);
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not probe the routes');
		} finally {
			probing = false;
		}
	}

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

	// The certificate waits for DNS: the reconciler concluded that the domain
	// does not reach this installation and nothing usable is on hand.
	function dnsPending(route: RouteStatus): boolean {
		return route.edge?.deferred === true;
	}

	function dnsTitle(route: RouteStatus): string {
		const edge = route.edge;
		if (!edge) return '';
		const parts: string[] = [edge.message ?? '', ...edge.addresses];
		if (edge.checked_at) parts.push(`checked ${formatDateTime(edge.checked_at)}`);
		return parts.filter(Boolean).join(' · ');
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
					{#if route.certificate || dnsPending(route)}
						<span class="ml-auto flex items-center gap-3">
							{#if dnsPending(route)}
								<span class="text-status-warning flex items-center gap-1.5" title={dnsTitle(route)}>
									<span class="size-[8px] rounded-full bg-status-warning"></span>DNS pending
								</span>
								<Button
									size="sm"
									variant="ghost"
									disabled={!mayProbe}
									busy={probing}
									title={probeTitle}
									onclick={probe}
								>
									<RadioTower size={12} /> Probe now
								</Button>
							{/if}
							{#if route.certificate}
								{@const meta = CERT_META[route.certificate.state]}
								<span class="flex items-center gap-1.5 {meta.text}" title={certTitle(route)}>
									<span class="size-[8px] rounded-full {meta.dot}"></span>{meta.label}
								</span>
							{/if}
						</span>
					{/if}
				</div>
			{/each}
		</div>
	</Card>
{/if}
