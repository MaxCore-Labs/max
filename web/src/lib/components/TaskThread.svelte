<script lang="ts">
	import { SvelteSet } from 'svelte/reactivity';
	import { api, type Schemas } from '#lib/api/client.js';
	import { t } from '#lib/i18n/vi.js';
	import { events as defaultStream, type EventStreamManager } from '#lib/state/events.svelte.js';
	import { EmptyState } from '#lib/ui/index.js';
	import MarkdownView from './MarkdownView.svelte';

	interface Props {
		taskId: string;
		initialEvents?: Schemas['TaskEvent'][];
		stream?: EventStreamManager;
		class?: string;
	}

	let {
		taskId,
		initialEvents = [],
		stream = defaultStream,
		class: className = ''
	}: Props = $props();

	let extraEvents = $state<Schemas['TaskEvent'][]>([]);

	let mergedEvents = $derived.by(() => {
		const seen = new SvelteSet<string>();
		const out: Schemas['TaskEvent'][] = [];
		for (const ev of [...initialEvents, ...extraEvents]) {
			if (!ev?.id || seen.has(ev.id)) continue;
			seen.add(ev.id);
			out.push(ev);
		}
		return out;
	});

	function appendIncoming(candidate: unknown): void {
		const maybeEv = candidate as Partial<Schemas['TaskEvent']> | undefined;
		if (maybeEv && typeof maybeEv.id === 'string' && typeof maybeEv.body === 'string') {
			const ev = maybeEv as Schemas['TaskEvent'];
			const allIds = new SvelteSet(mergedEvents.map((x) => x.id));
			if (!allIds.has(ev.id)) {
				extraEvents = [...extraEvents, ev];
			}
			return;
		}
		void api
			.listTaskEvents(taskId, { limit: 100 })
			.then((res) => {
				const allIds = new SvelteSet(mergedEvents.map((x) => x.id));
				const fresh = res.items.filter((x) => !allIds.has(x.id));
				if (fresh.length > 0) extraEvents = [...extraEvents, ...fresh];
			})
			.catch(() => {});
	}

	$effect(() => {
		const id = taskId;
		if (!id) return;
		const unsub = stream.subscribe([`task:${id}`], (ev) => {
			appendIncoming(ev.data);
		});
		return () => unsub();
	});
</script>

<div class="flex flex-col gap-3 {className}">
	{#if mergedEvents.length === 0}
		<EmptyState message={t('common.empty')} />
	{:else}
		{#each mergedEvents as ev (ev.id)}
			<article
				data-task-event={ev.id}
				class="rounded-[var(--radius)] border border-border-strong bg-surface p-3"
			>
				<div class="mb-1 flex items-center justify-between gap-2 text-xs text-muted">
					<span class="font-mono font-medium text-foreground">
						{ev.actorId ?? ev.actorKind} · {ev.kind}
					</span>
				</div>
				<MarkdownView source={ev.body} class="text-sm text-foreground" />
			</article>
		{/each}
	{/if}
</div>
