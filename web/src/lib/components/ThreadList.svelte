<script lang="ts">
	import type { Schemas } from '#lib/api/client.js';
	import { t } from '#lib/i18n/vi.js';
	import { Button, EmptyState } from '#lib/ui/index.js';
	import Plus from '@lucide/svelte/icons/plus';

	interface Props {
		items: Schemas['Thread'][];
		activeId: string | null;
		onselect: (id: string) => void;
		oncreate: () => void;
		class?: string;
	}

	let { items, activeId, onselect, oncreate, class: className = '' }: Props = $props();
</script>

<div class="flex flex-col gap-2 {className}">
	<div class="flex items-center justify-between gap-2">
		<h2 class="text-sm font-semibold text-foreground">{t('chat.threads')}</h2>
		<Button icon={Plus} onclick={oncreate}>{t('chat.new_thread')}</Button>
	</div>

	{#if items.length === 0}
		<EmptyState message={t('chat.empty')} />
	{:else}
		<div class="flex flex-col gap-0.5">
			{#each items as th (th.id)}
				<button
					type="button"
					onclick={() => onselect(th.id)}
					class="flex h-10 w-full cursor-pointer items-center justify-between rounded-[var(--radius)] px-3 text-left text-sm font-medium transition-colors {th.id ===
					activeId
						? 'bg-secondary text-foreground'
						: 'text-muted hover:bg-item-hover hover:text-foreground'}"
				>
					<span class="truncate">{th.title}</span>
				</button>
			{/each}
		</div>
	{/if}
</div>
