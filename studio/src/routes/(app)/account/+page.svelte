<script lang="ts">
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import PasswordCard from '$lib/components/security/PasswordCard.svelte';
	import TwoFactorCard from '$lib/components/security/TwoFactorCard.svelte';
	import SessionsCard from '$lib/components/security/SessionsCard.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
</script>

<svelte:head>
	<title>Account security — skali</title>
</svelte:head>

<PageHeader title="Account security">
	{#snippet subtitle()}
		Password, two-factor authentication, and active sessions for {data.user.email}
	{/snippet}
</PageHeader>

<div class="flex flex-col gap-4 pb-6">
	<PasswordCard />
	<TwoFactorCard enabled={data.user.two_factor_enabled} />
	<SessionsCard sessions={data.sessions} />
</div>
