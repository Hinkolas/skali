<script lang="ts">
	import Database from '@lucide/svelte/icons/database';
	import LayoutDashboard from '@lucide/svelte/icons/layout-dashboard';
	import SlidersHorizontal from '@lucide/svelte/icons/sliders-horizontal';
	import SubNav, { type SubNavTab } from '$lib/components/ui/SubNav.svelte';

	// The tabs of one database pool's pages; system pages carry no
	// environment, so the links are plain.
	let { pool }: { pool: string } = $props();

	const base = $derived(`/system/databases/${encodeURIComponent(pool)}`);
	const tabs = $derived<SubNavTab[]>([
		{ label: 'Overview', path: base, icon: LayoutDashboard, exact: true },
		{ label: 'Tuning', path: `${base}/tuning`, icon: SlidersHorizontal },
		{ label: 'Databases', path: `${base}/databases`, icon: Database }
	]);
</script>

<SubNav label="Database pool" {tabs} />
