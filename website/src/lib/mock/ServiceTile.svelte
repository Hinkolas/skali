<script lang="ts" module>
	import Container from '@lucide/svelte/icons/container';
	import Database from '@lucide/svelte/icons/database';
	import HardDrive from '@lucide/svelte/icons/hard-drive';

	export type Kind = 'application' | 'database' | 'bucket';

	// Icon, color and tint per service kind, as in the Studio's TypeBadge.
	const kinds = {
		application: { icon: Container, color: 'text-service-app', tint: 'bg-service-app/15' },
		database: { icon: Database, color: 'text-service-db', tint: 'bg-service-db/12' },
		bucket: { icon: HardDrive, color: 'text-service-storage', tint: 'bg-service-storage/12' }
	};
</script>

<script lang="ts">
	let { kind, size = 28.6 }: { kind: Kind; size?: number } = $props();
	const { icon: Icon, color, tint } = $derived(kinds[kind]);
</script>

<span
	class="flex shrink-0 items-center justify-center {color} {tint}"
	style:width="{size}px"
	style:height="{size}px"
	style:border-radius="{size > 30 ? 10 : 8}px"
>
	<Icon size={size > 30 ? 16 : 14} strokeWidth={2} />
</span>
