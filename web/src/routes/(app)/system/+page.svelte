<script lang="ts">
	import EngineSettings from '#lib/components/EngineSettings.svelte';
	import HealthDots from '#lib/components/HealthDots.svelte';
	import { t } from '#lib/i18n/vi.js';
	import { systemStatus } from '#lib/state/status.svelte.js';
	import { Card, Skeleton } from '#lib/ui/index.js';

	$effect(() => {
		void systemStatus.load();
	});
</script>

<div class="mx-auto flex max-w-4xl flex-col gap-6">
	<h1 class="text-xl font-semibold text-foreground">{t('system.title')}</h1>

	{#if systemStatus.loading && !systemStatus.status}
		<Card><Skeleton lines={4} /></Card>
	{:else if systemStatus.status && systemStatus.settings}
		<Card title={t('system.deps')}>
			<div class="flex flex-col gap-4">
				<HealthDots engines={systemStatus.status.engines} deps={systemStatus.status.deps} />
				<div class="text-sm text-muted tabular-nums">
					Tổng việc đang xếp hàng: <b class="font-semibold text-foreground">{systemStatus.status.queue.total}</b>
				</div>
			</div>
		</Card>

		<EngineSettings
			defaultEngine={systemStatus.settings.defaultEngine}
			settings={systemStatus.settings.engines}
			statuses={systemStatus.status.engines}
			onsaved={() => void systemStatus.load()}
		/>
	{/if}
</div>
