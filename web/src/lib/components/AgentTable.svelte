<script lang="ts">
	import type { Schemas } from '#lib/api/client.js';
	import { DataTable, StatusBadge, type DataColumn } from '#lib/ui/index.js';

	interface Props {
		agents: Schemas['Agent'][];
		onLoadMore?: () => void;
		viewportHeight?: number;
		class?: string;
	}

	let { agents, onLoadMore, viewportHeight = 280, class: className = '' }: Props = $props();

	const columns: DataColumn<Schemas['Agent']>[] = [
		{ key: 'name', labelKey: 'agents.col.name', cell: nameCell },
		{ key: 'templateSlug', labelKey: 'agents.col.template', cell: templateCell },
		{ key: 'engines', labelKey: 'agents.col.engines', cell: enginesCell },
		{ key: 'status', labelKey: 'agents.col.status', cell: statusCell }
	];
</script>

{#snippet nameCell(row: Schemas['Agent'])}
	<a href="/agents/{row.id}" class="text-foreground no-underline hover:underline">
		{row.name} · {row.slug}
	</a>
{/snippet}

{#snippet templateCell(row: Schemas['Agent'])}
	{row.templateSlug ?? '—'}
{/snippet}

{#snippet enginesCell(row: Schemas['Agent'])}
	{row.engines.join(', ')}
{/snippet}

{#snippet statusCell(row: Schemas['Agent'])}
	<StatusBadge kind="agent" status={row.status} />
{/snippet}

<DataTable
	rows={agents}
	{columns}
	rowKey={(r) => r.id}
	onEndReached={onLoadMore}
	{viewportHeight}
	class={className}
/>
