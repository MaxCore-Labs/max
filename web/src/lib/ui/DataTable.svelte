<script module lang="ts">
	import type { Snippet } from 'svelte';

	export interface DataColumn<Row> {
		key: string;
		labelKey: string;
		align?: 'left' | 'right';
		width?: string;
		cell?: Snippet<[Row]>;
	}
</script>

<script lang="ts" generics="T">
	import { t } from '#lib/i18n/vi.js';

	interface Props {
		rows: T[];
		columns: DataColumn<T>[];
		rowKey: (row: T) => string | number;
		onEndReached?: () => void;
		viewportHeight?: number;
		class?: string;
	}

	const ROW_H = 40;
	const OVERSCAN = 5;

	let {
		rows,
		columns,
		rowKey,
		onEndReached,
		viewportHeight = 480,
		class: className = ''
	}: Props = $props();

	let scrollTop = $state(0);
	let endLatched = false;

	let startIdx = $derived(Math.max(0, Math.floor(scrollTop / ROW_H) - OVERSCAN));
	let visibleCount = $derived(Math.ceil(viewportHeight / ROW_H) + OVERSCAN * 2);
	let endIdx = $derived(Math.min(rows.length, startIdx + visibleCount));
	let visibleRows = $derived(rows.slice(startIdx, endIdx));
	let topPad = $derived(startIdx * ROW_H);
	let bottomPad = $derived(Math.max(0, (rows.length - endIdx) * ROW_H));

	function spacerHeight(px: () => number) {
		return (el: HTMLTableRowElement) => {
			$effect(() => {
				el.style.height = `${px()}px`;
			});
		};
	}

	function maxScrollerHeight(px: () => number) {
		return (el: HTMLDivElement) => {
			$effect(() => {
				el.style.maxHeight = `${px()}px`;
			});
		};
	}

	function handleScroll(ev: Event): void {
		const el = ev.currentTarget as HTMLDivElement;
		scrollTop = el.scrollTop;
		const nearBottom = el.scrollTop + el.clientHeight >= el.scrollHeight - ROW_H * 2;
		if (nearBottom) {
			if (!endLatched) {
				endLatched = true;
				onEndReached?.();
			}
		} else {
			endLatched = false;
		}
	}

	function readProp(row: T, key: string): string {
		const v = (row as Record<string, unknown>)[key];
		return v === null || v === undefined ? '' : String(v);
	}
</script>

<div
	data-table-scroller
	onscroll={handleScroll}
	{@attach maxScrollerHeight(() => viewportHeight)}
	class="overflow-auto {className}"
>
	<table class="w-full min-w-160 border-collapse text-sm">
		<thead class="sticky top-0 z-10 bg-surface">
			<tr>
				{#each columns as col, idx (col.key)}
					<th
						class="h-10 border-b border-border-strong bg-surface px-4 text-xs font-medium whitespace-nowrap text-muted sm:px-5 {col.align ===
						'right'
							? 'text-right tabular-nums'
							: 'text-left'} {idx === 0 ? 'sticky left-0 z-20' : ''}"
					>
						{t(col.labelKey)}
					</th>
				{/each}
			</tr>
		</thead>
		<tbody>
			{#if topPad > 0}
				<tr aria-hidden="true" {@attach spacerHeight(() => topPad)}>
					<td colspan={columns.length} class="border-0 p-0"></td>
				</tr>
			{/if}
			{#each visibleRows as row (rowKey(row))}
				<tr class="group hover:bg-surface-hover">
					{#each columns as col, idx (col.key)}
						<td
							class="h-10 border-b border-border-strong bg-surface px-4 whitespace-nowrap group-last:border-b-0 group-hover:bg-surface-hover sm:px-5 {col.align ===
							'right'
								? 'text-right tabular-nums'
								: ''} {idx === 0 ? 'sticky left-0 z-10 max-w-55 truncate font-medium' : ''}"
						>
							{#if col.cell}
								{@render col.cell(row)}
							{:else}
								{readProp(row, col.key)}
							{/if}
						</td>
					{/each}
				</tr>
			{/each}
			{#if bottomPad > 0}
				<tr aria-hidden="true" {@attach spacerHeight(() => bottomPad)}>
					<td colspan={columns.length} class="border-0 p-0"></td>
				</tr>
			{/if}
		</tbody>
	</table>
</div>
