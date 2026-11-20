<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		title: string;
		meta?: string;
		initials?: string;
		href?: string;
		class?: string;
		onclick?: () => void;
		trailing?: Snippet;
	}

	let { title, meta, initials, href, class: className = '', onclick, trailing }: Props = $props();
</script>

{#snippet inner()}
	{#if initials}
		<span
			class="inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-[var(--radius)] bg-background text-sm font-semibold text-foreground group-hover:bg-surface"
		>
			{initials}
		</span>
	{/if}
	<div class="min-w-0 flex-1">
		<p class="truncate text-sm font-medium text-foreground">{title}</p>
		{#if meta}
			<p class="mt-0.5 truncate text-xs text-muted">{meta}</p>
		{/if}
	</div>
	{#if trailing}
		<div class="shrink-0">{@render trailing()}</div>
	{/if}
{/snippet}

{#if href}
	<a
		{href}
		{onclick}
		class="group flex min-w-0 items-center gap-3 rounded-[var(--radius)] px-3 py-2 text-left no-underline transition-colors hover:bg-item-hover {className}"
	>
		{@render inner()}
	</a>
{:else if onclick}
	<button
		type="button"
		{onclick}
		class="group flex w-full min-w-0 cursor-pointer items-center gap-3 rounded-[var(--radius)] px-3 py-2 text-left transition-colors hover:bg-item-hover {className}"
	>
		{@render inner()}
	</button>
{:else}
	<div
		class="group flex min-w-0 items-center gap-3 rounded-[var(--radius)] px-3 py-2 transition-colors hover:bg-item-hover {className}"
	>
		{@render inner()}
	</div>
{/if}
