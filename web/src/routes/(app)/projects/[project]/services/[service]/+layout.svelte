<script lang="ts">
	import type { Snippet } from 'svelte';
	import ServiceHeader from '$lib/components/service/ServiceHeader.svelte';
	import ServiceTabs from '$lib/components/service/ServiceTabs.svelte';
	import type { LayoutData } from './$types';

	let { data, children }: { data: LayoutData; children: Snippet } = $props();
</script>

<ServiceHeader service={data.service} />
<ServiceTabs project={data.project} service={data.service} />

{@render children()}
