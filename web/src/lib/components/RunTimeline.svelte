<script lang="ts">
	import type { Schemas } from '#lib/api/client.js';
	import { t } from '#lib/i18n/vi.js';
	import { EmptyState } from '#lib/ui/index.js';
	import ToolChip from './ToolChip.svelte';

	interface Props {
		events: Schemas['RunEvent'][];
		class?: string;
	}

	interface ParsedEvent {
		id: number;
		type: string;
		toolName: string;
		verdict: string;
		reason: string;
		preview: string;
	}

	let { events, class: className = '' }: Props = $props();

	let parsed = $derived.by<ParsedEvent[]>(() =>
		events.map((ev) => {
			let obj: Record<string, unknown> = {};
			try {
				obj = JSON.parse(ev.data) as Record<string, unknown>;
			} catch {
				obj = { preview: ev.data };
			}
			const toolName = String(obj.toolName ?? obj.tool ?? obj.name ?? ev.type);
			const verdict = String(obj.verdict ?? obj.decision ?? obj.status ?? '');
			const reason = String(obj.reason ?? obj.message ?? '');
			const preview = String(obj.preview ?? obj.inputPreview ?? '');
			return {
				id: ev.id,
				type: ev.type,
				toolName,
				verdict,
				reason,
				preview
			};
		})
	);
</script>

<div class="flex flex-col gap-2 {className}">
	{#if parsed.length === 0}
		<EmptyState message={t('common.empty')} />
	{:else}
		<ol class="flex flex-col gap-2">
			{#each parsed as item (item.id)}
				<li
					class="flex flex-wrap items-center justify-between gap-2 rounded-[var(--radius-sm)] border border-border bg-surface px-3 py-2 text-xs"
				>
					<div class="flex flex-wrap items-center gap-2">
						<ToolChip label={item.toolName} class="mt-0" />
						{#if item.verdict}
							<span class="rounded-[var(--radius-full)] bg-secondary px-2 py-0.5 font-mono font-semibold text-foreground">
								{item.verdict}
							</span>
						{/if}
						{#if item.reason}
							<span class="text-muted">{item.reason}</span>
						{/if}
					</div>
					{#if item.preview}
						<span class="max-w-xs truncate font-mono text-muted">{item.preview}</span>
					{/if}
				</li>
			{/each}
		</ol>
	{/if}
</div>
