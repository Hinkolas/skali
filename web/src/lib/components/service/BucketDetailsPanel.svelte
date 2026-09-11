<script lang="ts">
	import type { BucketView } from '$lib/models/service';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import { HEALTH_META } from '$lib/service-types';
	import { formatBytes, formatCount } from '$lib/format';
	import { describeSeconds } from '$lib/cron';
	import Card from '$lib/components/ui/Card.svelte';
	import KeyValueRow from '$lib/components/ui/KeyValueRow.svelte';

	// What the bucket claim asked for: who may read it, whether versions are
	// kept, the quotas that cap it, and the lifecycle rules that clean up
	// after clients. Optional fields render as their platform default so the
	// reader never has to guess what an absent line means.
	let { service }: { service: BucketView } = $props();

	const live = $derived(envStatus.service('bucket', service.key));
	const meta = $derived(HEALTH_META[live?.health ?? 'unknown']);
	const config = $derived(service.config);

	const visibility = $derived(
		config.visibility === 'public' ? 'public · anonymous reads' : 'private · keypair required'
	);
	const quota = $derived(config.storageQuotaBytes ? formatBytes(config.storageQuotaBytes) : 'none');
	const objects = $derived(
		config.objectQuota ? `${formatCount(config.objectQuota)} objects` : 'unlimited'
	);
	const maxObject = $derived(
		config.maxObjectSizeBytes ? formatBytes(config.maxObjectSizeBytes) : 'no limit'
	);
	const abortUploads = $derived(
		config.abortIncompleteUploadsAfterSeconds
			? `after ${describeSeconds(config.abortIncompleteUploadsAfterSeconds)}`
			: 'never'
	);
	const expireVersions = $derived(
		config.versioning !== 'enabled'
			? 'n/a · versioning off'
			: config.expireNoncurrentVersionsAfterSeconds
				? `after ${describeSeconds(config.expireNoncurrentVersionsAfterSeconds)}`
				: 'kept forever'
	);
</script>

<Card class="flex flex-col p-5">
	<div class="mb-3.5 flex items-center gap-2.5">
		<h3 class="text-text-primary text-xl font-semibold">Bucket</h3>
		<span class="flex items-center gap-1.5 text-md {meta.text}">
			<span class="size-[8px] rounded-full {meta.dot}"></span>{meta.label.toLowerCase()}
		</span>
	</div>
	<div class="flex flex-col">
		<KeyValueRow k="Visibility" v={visibility} labelWidth="w-38" />
		<KeyValueRow k="Versioning" v={config.versioning} labelWidth="w-38" />
		<KeyValueRow k="Storage quota" v={quota} labelWidth="w-38" />
		<KeyValueRow k="Object quota" v={objects} labelWidth="w-38" />
		<KeyValueRow k="Max object size" v={maxObject} labelWidth="w-38" />
		<KeyValueRow k="Abort stale uploads" v={abortUploads} labelWidth="w-38" />
		<KeyValueRow k="Expire old versions" v={expireVersions} labelWidth="w-38" />
	</div>
</Card>
