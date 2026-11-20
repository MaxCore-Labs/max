<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import HealthDots from '#lib/components/HealthDots.svelte';
	import { t } from '#lib/i18n/vi.js';
	import { events } from '#lib/state/events.svelte.js';
	import { inbox } from '#lib/state/inbox.svelte.js';
	import { session } from '#lib/state/session.svelte.js';
	import { systemStatus } from '#lib/state/status.svelte.js';
	import { AppShell, Button } from '#lib/ui/index.js';
	import type { Snippet } from 'svelte';

	let { children }: { children: Snippet } = $props();

	$effect(() => {
		let unsub: (() => void) | null = null;
		void session.refresh().then((me) => {
			if (!me) {
				void goto('/login');
				return;
			}
			void inbox.load();
			void systemStatus.load();
			unsub = events.subscribe(
				['status', 'inbox'],
				(ev) => {
					if (ev.topic === 'inbox') void inbox.load();
					if (ev.topic === 'status') void systemStatus.load();
				},
				() => {
					void inbox.load();
					void systemStatus.load();
				}
			);
		});
		return () => unsub?.();
	});

	async function handleLogout(): Promise<void> {
		await session.logout();
		await goto('/login');
	}
</script>

<AppShell current={page.url.pathname} inboxCount={inbox.unreadCount}>
	{#snippet topbar()}
		{#if systemStatus.status}
			<HealthDots engines={systemStatus.status.engines} deps={systemStatus.status.deps} />
		{:else}
			<span></span>
		{/if}
		<Button variant="ghost" onclick={handleLogout}>{t('auth.logout')}</Button>
	{/snippet}
	{@render children()}
</AppShell>
