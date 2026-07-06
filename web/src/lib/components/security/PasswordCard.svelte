<script lang="ts">
	import KeyRound from '@lucide/svelte/icons/key-round';
	import { invalidateAll } from '$app/navigation';
	import { modal } from '$lib/stores/modal.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import ChangePasswordModal, {
		modalOptions as changePasswordOptions
	} from '$lib/components/security/ChangePasswordModal.svelte';

	async function changePassword() {
		if (await modal.open<boolean>(ChangePasswordModal, {}, changePasswordOptions).result) {
			await invalidateAll(); // the session list shrinks to just this one
		}
	}
</script>

<Card class="flex items-center gap-4 px-5.5 py-4.5">
	<div
		class="bg-surface-input text-text-tertiary grid size-9 flex-none place-items-center rounded-[10px]"
	>
		<KeyRound size={16} strokeWidth={1.75} />
	</div>
	<div class="min-w-0 flex-1">
		<h2 class="text-text-primary text-[14px] font-semibold tracking-tight">Password</h2>
		<p class="text-text-muted mt-0.5 text-[12.5px]">
			Changing it signs out every other session; only this one survives.
		</p>
	</div>
	<Button variant="secondary" onclick={changePassword}>Change password</Button>
</Card>
