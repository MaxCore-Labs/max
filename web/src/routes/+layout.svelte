<script lang="ts">
	import '../app.css';
	import Toast from '#lib/ui/Toast.svelte';
	import type { Snippet } from 'svelte';

	let { children }: { children: Snippet } = $props();
</script>

{@render children()}
<Toast />
