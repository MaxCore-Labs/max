<script lang="ts">
	import type { Schemas } from '#lib/api/client.js';
	import { Button, StatusBadge } from '#lib/ui/index.js';
	import RunTimeline from './RunTimeline.svelte';

	interface Props {
		run: Schemas['Run'];
		expanded?: boolean;
		events?: Schemas['RunEvent'][];
		ontoggle?: () => void;
		class?: string;
	}

	let {
		run,
		expanded = false,
		events = [],
		ontoggle,
		class: className = ''
	}: Props = $props();

	let durationSec = $derived(
		run.endedAt ? Math.max(0, Math.round((run.endedAt - run.startedAt) / 1000)) : null
	);
</script>

<div class="rounded-[var(--radius)] border border-border-strong bg-surface p-3 {className}">
	<div class="flex flex-wrap items-center justify-between gap-3">
		<div class="flex min-w-0 flex-wrap items-center gap-2.5">
			<StatusBadge kind="run" status={run.status} />
			<a
				href="/agents/{run.agentId}"
				class="font-mono text-xs font-semibold text-foreground no-underline hover:underline"
			>
				{run.agentId}
			</a>
			<span class="font-mono text-xs text-muted">{run.engine}</span>
			{#if run.summary}
				<span class="max-w-md truncate text-sm text-foreground">{run.summary}</span>
			{/if}
		</div>

		<div class="flex items-center gap-3 text-xs text-muted tabular-nums">
			{#if durationSec !== null}
				<span>{durationSec}s</span>
			{/if}
			<span class="font-mono">{run.inputTokens + run.outputTokens} tok</span>
			<Button variant="ghost" onclick={ontoggle}>
				{expanded ? 'Ẩn chi tiết' : 'Chi tiết'}
			</Button>
		</div>
	</div>

	{#if expanded}
		<div class="mt-3 border-t border-border pt-3">
			<RunTimeline {events} />
		</div>
	{/if}
</div>
