<script lang="ts">
	import '../app.css';
	import { page } from '$app/state';
	import { liveEvents } from '#lib/events.svelte.js';
	import type { Snippet } from 'svelte';

	let { children }: { children: Snippet } = $props();

	$effect(() => {
		return liveEvents.connect();
	});
</script>

<div class="min-h-screen bg-background text-foreground">
	<header class="bg-surface border-b border-border-strong">
		<div class="mx-auto flex h-14 max-w-5xl items-center justify-between px-4 sm:px-6">
			<div class="flex items-center gap-6">
				<span class="text-base font-semibold">Max S3</span>
				<nav class="flex items-center gap-1" aria-label="Điều hướng chính">
					<a
						href="/"
						class="inline-flex h-10 items-center rounded-[var(--radius)] px-3 text-sm font-medium transition-colors {page.url.pathname === '/' ? 'bg-secondary text-foreground' : 'text-muted hover:bg-item-hover hover:text-foreground'}"
					>
						Trực tiếp (SSE)
					</a>
					<a
						href="/activity"
						class="inline-flex h-10 items-center rounded-[var(--radius)] px-3 text-sm font-medium transition-colors {page.url.pathname === '/activity' ? 'bg-secondary text-foreground' : 'text-muted hover:bg-item-hover hover:text-foreground'}"
					>
						Hoạt động &amp; UI
					</a>
				</nav>
			</div>

			<span
				data-testid="sse-status"
				class="inline-flex items-center gap-1.5 rounded-[var(--radius-full)] px-2.5 py-1 text-xs font-medium {liveEvents.connected ? 'bg-success-bg text-success' : 'bg-neutral-bg text-neutral'}"
			>
				{liveEvents.connected ? 'SSE đã kết nối' : 'Đang nối SSE...'}
			</span>
		</div>
	</header>

	<main class="mx-auto max-w-5xl p-4 sm:p-6">
		{@render children()}
	</main>
</div>
