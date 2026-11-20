<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { session } from '#lib/state/session.svelte.js';
	import { AppShell } from '#lib/ui/index.js';
	import type { Snippet } from 'svelte';

	let { children }: { children: Snippet } = $props();

	$effect(() => {
		if (!session.checked && !session.loading) {
			void session.refresh().then((me) => {
				if (!me) void goto('/login');
			});
		}
	});
</script>

<AppShell current={page.url.pathname}>
	{@render children()}
</AppShell>
