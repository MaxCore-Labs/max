<script lang="ts">
	import type { Schemas } from '#lib/api/client.js';
	import { t } from '#lib/i18n/vi.js';

	interface Props {
		quota: Schemas['EngineQuotaInfo'];
		shortHours?: number;
		backgroundShare?: number;
		queuedCount?: number;
		class?: string;
	}

	let {
		quota,
		shortHours = 5,
		backgroundShare = 0.7,
		queuedCount = 0,
		class: className = ''
	}: Props = $props();

	function fmtTime(ms: number): string {
		const d = new Date(ms);
		const hh = String(d.getHours()).padStart(2, '0');
		const mm = String(d.getMinutes()).padStart(2, '0');
		return `${hh}:${mm}`;
	}

	let isLimited = $derived(Boolean(quota.limitedUntil && quota.limitedUntil > Date.now()));
	let hasCeiling = $derived(Boolean(quota.estShort && quota.estShort > 0));
	let pct = $derived(
		hasCeiling && quota.estShort
			? Math.min(100, Math.max(0, Math.round((quota.usedShort / quota.estShort) * 100)))
			: 0
	);
	let markPct = $derived(Math.round(backgroundShare * 100));

	function setWidth(p: () => number) {
		return (el: HTMLElement) => {
			$effect(() => {
				el.style.width = `${p()}%`;
			});
		};
	}

	function setLeft(p: () => number) {
		return (el: HTMLElement) => {
			$effect(() => {
				el.style.left = `${p()}%`;
			});
		};
	}
</script>

<div class="flex flex-col gap-2 {className}">
	<div class="flex items-center justify-between gap-3 text-sm">
		<span class="font-medium text-foreground">{quota.engine} · cửa sổ {shortHours} giờ</span>
		<span class="text-right text-muted tabular-nums">
			{#if isLimited && quota.limitedUntil}
				{t('quota.reopen_at', { time: fmtTime(quota.limitedUntil) })}
			{:else if !hasCeiling}
				{t('quota.learning')}
			{:else}
				{pct}%
			{/if}
		</span>
	</div>

	<div class="relative h-2 overflow-hidden rounded-[var(--radius-full)] bg-background-hover">
		{#if isLimited}
			<div {@attach setWidth(() => 100)} class="h-full rounded-[var(--radius-full)] bg-neutral"></div>
		{:else if hasCeiling}
			<div {@attach setWidth(() => pct)} class="h-full rounded-[var(--radius-full)] bg-primary"></div>
			<div {@attach setLeft(() => markPct)} class="absolute top-0 bottom-0 w-0.5 bg-surface"></div>
		{/if}
	</div>

	<p class="text-xs text-muted">
		{#if isLimited}
			{t('quota.waiting_note', { count: queuedCount })}
		{:else if !hasCeiling}
			{t('quota.learning_note', { tokens: quota.usedShort.toLocaleString('vi-VN') })}
		{:else}
			Việc nền dừng ở {markPct}%.
		{/if}
	</p>
</div>
