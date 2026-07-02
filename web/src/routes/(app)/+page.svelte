<!--
	Deliberately unstyled placeholder: proves the whole auth chain
	(cookie -> hooks -> layout guard -> proxy -> API) end to end.
	The real dashboard arrives with the UI design phase.
-->
<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { signOut } from '$lib/stores/auth.svelte';
	import type { SessionInfo } from '$lib/types/auth';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	let sessions = $state<SessionInfo[]>([]);
	let error = $state('');

	onMount(load);

	async function load() {
		try {
			({ sessions } = await api.get<{ sessions: SessionInfo[] }>('/v1/auth/sessions'));
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function revoke(id: string) {
		try {
			await api.del(`/v1/auth/sessions/${id}`);
			await load();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}
</script>

<svelte:head>
	<title>skali</title>
</svelte:head>

<main class="mx-auto max-w-2xl p-8">
	<h1 class="text-xl font-semibold">skali</h1>
	<p class="mt-2 text-text-secondary">
		Signed in as {data.user.email}
		{#if data.user.name}({data.user.name}){/if}
	</p>

	<h2 class="mt-8 font-medium">Active sessions</h2>
	{#if error}
		<p class="mt-2 text-status-danger">{error}</p>
	{/if}
	<ul class="mt-2 flex flex-col gap-2">
		{#each sessions as session (session.id)}
			<li class="flex items-center justify-between gap-4 border border-border-default p-3">
				<div>
					<div>{session.user_agent || 'unknown client'}</div>
					<div class="text-text-muted">
						{session.ip_address} · expires {new Date(session.expires_at).toLocaleString()}
						{#if session.current}· current{/if}
					</div>
				</div>
				{#if !session.current}
					<button
						type="button"
						class="cursor-pointer border border-border-default px-3 py-1 hover:border-status-danger hover:text-status-danger"
						onclick={() => revoke(session.id)}
					>
						Revoke
					</button>
				{/if}
			</li>
		{/each}
	</ul>

	<button
		type="button"
		class="mt-8 cursor-pointer border border-border-default px-3 py-1 hover:bg-surface-overlay"
		onclick={signOut}
	>
		Sign out
	</button>
</main>
