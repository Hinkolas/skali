<script lang="ts">
	import Breadcrumbs from './Breadcrumbs.svelte';
	import SearchButton from './SearchButton.svelte';
	import UserMenu from './UserMenu.svelte';
</script>

<!-- Chromeless on purpose: sits on the page glow like the flat sidebar, so
     there is no seam between the two. Height matches the sidebar logo row. -->
<header class="flex h-14 flex-none items-center gap-3">
	<Breadcrumbs />
	<div class="flex-1"></div>
	<SearchButton />
	<UserMenu />
</header>
