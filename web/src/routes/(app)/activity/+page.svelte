<script lang="ts">
	import { SvelteSet } from 'svelte/reactivity';
	import RunRow from '#lib/components/RunRow.svelte';
	import { t } from '#lib/i18n/vi.js';
	import { runsState, type ActivityTimeRange } from '#lib/state/runs.svelte.js';
	import { Button, Card, EmptyState, Field, Select, Skeleton } from '#lib/ui/index.js';

	let expandedIds = new SvelteSet<string>();

	$effect(() => {
		void runsState.load();
	});

	async function toggleRun(runId: string): Promise<void> {
		if (expandedIds.has(runId)) {
			expandedIds.delete(runId);
			return;
		}
		expandedIds.add(runId);
		await runsState.loadRunEvents(runId);
	}
</script>

<div class="flex flex-col gap-4">
	<div class="flex flex-wrap items-center justify-between gap-3">
		<h1 class="text-lg font-semibold text-foreground">{t('activity.title')}</h1>
	</div>

	<Card>
		<div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
			<Field label={t('activity.range_label')}>
				<Select
					value={runsState.timeRange}
					options={[
						{ value: '1h', label: t('activity.range.1h') },
						{ value: '24h', label: t('activity.range.24h') },
						{ value: '7d', label: t('activity.range.7d') },
						{ value: '30d', label: t('activity.range.30d') }
					]}
					onchange={(ev) =>
						void runsState.setTimeRange(
							(ev.currentTarget as HTMLSelectElement).value as ActivityTimeRange
						)}
				/>
			</Field>

			<Field label={t('agents.col.status')}>
				<Select
					value={runsState.statusFilter}
					options={[
						{ value: '', label: t('agents.all_statuses') },
						{ value: 'running', label: t('status.run.running') },
						{ value: 'succeeded', label: t('status.run.succeeded') },
						{ value: 'parked', label: t('status.run.parked') },
						{ value: 'failed', label: t('status.run.failed') },
						{ value: 'quota_stopped', label: t('status.run.quota_stopped') }
					]}
					onchange={(ev) =>
						void runsState.setStatus((ev.currentTarget as HTMLSelectElement).value)}
				/>
			</Field>
		</div>
	</Card>

	{#if runsState.loading && runsState.runs.length === 0}
		<Skeleton lines={5} />
	{:else if runsState.error}
		<Card><p class="text-sm text-error-text">{runsState.error}</p></Card>
	{:else if runsState.runs.length === 0}
		<Card><EmptyState message={t('common.empty')} /></Card>
	{:else}
		<div class="flex flex-col gap-2">
			{#each runsState.runs as r (r.id)}
				<RunRow
					run={r}
					expanded={expandedIds.has(r.id)}
					events={runsState.eventsByRun.get(r.id) ?? []}
					ontoggle={() => void toggleRun(r.id)}
				/>
			{/each}
		</div>

		{#if runsState.nextCursor}
			<div class="flex justify-center">
				<Button
					variant="outline"
					loading={runsState.loadingMore}
					onclick={() => void runsState.loadMore()}
				>
					Tải thêm
				</Button>
			</div>
		{/if}
	{/if}
</div>
