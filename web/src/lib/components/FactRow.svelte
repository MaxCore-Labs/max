<script lang="ts">
	import type { Schemas } from '#lib/api/client.js';

	interface Props {
		fact: Schemas['FactItem'];
	}

	let { fact }: Props = $props();
</script>

<div class="flex flex-wrap items-start justify-between gap-2 py-2.5 text-sm">
	<div class="flex flex-col gap-0.5">
		<div class="flex items-center gap-2">
			<span class="font-mono text-xs font-medium text-foreground">{fact.slug}</span>
			<span class="rounded-full bg-neutral-bg px-2 py-0.5 text-xs text-neutral">{fact.type}</span>
			{#if fact.supersededBy}
				<span class="rounded-full bg-warning-bg px-2 py-0.5 text-xs text-warning">
					Đã thay bởi {fact.supersededBy}
				</span>
			{/if}
		</div>
		<p class="text-foreground">{fact.statement}</p>
	</div>
	<span class="font-mono text-xs text-muted tabular-nums">
		tin cậy {fact.confidence}/5 · {fact.evidenceMsgId}
	</span>
</div>
