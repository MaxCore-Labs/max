<script lang="ts">
	import { t } from '#lib/i18n/vi.js';
	import X from '@lucide/svelte/icons/x';
	import { toast } from './toast.svelte.js';
</script>

{#if toast.items.length > 0}
	<div
		aria-live="polite"
		class="fixed right-4 bottom-4 z-50 flex max-w-sm flex-col gap-2"
	>
		{#each toast.items as item (item.id)}
			<div
				role={item.kind === 'error' ? 'alert' : 'status'}
				class="flex items-center justify-between gap-3 rounded-[var(--radius)] border px-4 py-3 text-sm shadow-popover {item.kind ===
				'error'
					? 'border-error bg-error-bg text-error-strong'
					: 'border-border-strong bg-surface-overlay text-foreground'}"
			>
				<span>{t(item.messageKey)}</span>
				<button
					type="button"
					aria-label={t('common.close')}
					onclick={() => toast.dismiss(item.id)}
					class="inline-flex h-6 w-6 shrink-0 cursor-pointer items-center justify-center rounded-[var(--radius-sm)] text-muted hover:bg-item-hover hover:text-foreground"
				>
					<X size={14} strokeWidth={2} aria-hidden="true" />
				</button>
			</div>
		{/each}
	</div>
{/if}
