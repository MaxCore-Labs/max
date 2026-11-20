<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		title?: string;
		flush?: boolean;
		class?: string;
		action?: Snippet;
		children: Snippet;
	}

	let { title, flush = false, class: className = '', action, children }: Props = $props();
</script>

<section
	class="flex min-w-0 flex-col rounded-[var(--radius-lg)] border border-border bg-surface {flush
		? 'overflow-hidden p-0'
		: 'p-4 sm:p-5'} {className}"
>
	{#if title || action}
		<div
			class="flex items-start justify-between gap-3 {flush
				? 'border-b border-border px-4 pt-4 pb-3 sm:px-5'
				: 'mb-4'}"
		>
			{#if title}
				<h3
					class="min-w-0 text-base font-semibold text-balance {action
						? 'flex min-h-10 items-center'
						: ''}"
				>
					{title}
				</h3>
			{/if}
			{#if action}
				<div class="shrink-0">{@render action()}</div>
			{/if}
		</div>
	{/if}
	{@render children()}
</section>
