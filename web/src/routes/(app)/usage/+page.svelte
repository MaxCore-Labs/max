<script lang="ts">
	import QuotaMeter from '#lib/components/QuotaMeter.svelte';
	import { t } from '#lib/i18n/vi.js';
	import { systemStatus } from '#lib/state/status.svelte.js';
	import { usage } from '#lib/state/usage.svelte.js';
	import { Card, DataTable, EmptyState, Skeleton } from '#lib/ui/index.js';

	$effect(() => {
		void usage.load(7);
		void systemStatus.load();
	});

	let queuedTotal = $derived(systemStatus.status?.queue.total ?? 0);
</script>

<div class="mx-auto flex max-w-5xl flex-col gap-6">
	<h1 class="text-xl font-semibold text-foreground">{t('usage.title')}</h1>

	{#if usage.loading && !usage.quota}
		<Card><Skeleton lines={3} /></Card>
	{:else if usage.quota}
		<div class="grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-3">
			{#each usage.quota.engines as q (q.engine)}
				<Card>
					<QuotaMeter
						quota={q}
						shortHours={usage.quota.shortHours}
						backgroundShare={usage.quota.backgroundShare}
						queuedCount={queuedTotal}
					/>
				</Card>
			{/each}
		</div>
	{/if}

	<Card title="Mức dùng theo ngày (7 ngày gần nhất)" flush>
		{#if usage.loading && usage.rows.length === 0}
			<div class="p-4"><Skeleton lines={4} /></div>
		{:else if usage.rows.length === 0}
			<EmptyState message={t('common.empty')} />
		{:else}
			<DataTable
				rows={usage.rows}
				rowKey={(r) => `${r.day}-${r.agentId}-${r.engine}`}
				columns={[
					{ key: 'day', labelKey: 'Ngày' },
					{ key: 'agentId', labelKey: 'Agent' },
					{ key: 'teamId', labelKey: 'Team' },
					{ key: 'engine', labelKey: 'Engine' },
					{ key: 'runs', labelKey: 'Số lượt', align: 'right' },
					{ key: 'tokens', labelKey: 'Token', align: 'right' }
				]}
			/>
		{/if}
	</Card>
</div>
