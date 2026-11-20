<script lang="ts">
	import type { Schemas } from '#lib/api/client.js';
	import { Tree, type TreeNode } from '#lib/ui/index.js';

	interface Props {
		teams: Schemas['Team'][];
		agents?: Schemas['Agent'][];
		selectedTeam?: string;
		onSelectTeam?: (teamSlug: string) => void;
		class?: string;
	}

	let {
		teams,
		agents = [],
		selectedTeam = '',
		onSelectTeam,
		class: className = ''
	}: Props = $props();

	let expanded = $state<string[]>([]);

	let nodes = $derived<TreeNode[]>(
		teams.map((tm) => ({
			id: tm.slug,
			label: `${tm.name} (${tm.agentCount})`,
			children: agents
				.filter((a) => a.teamId === tm.id)
				.slice(0, 25)
				.map((a) => ({
					id: `agent:${a.id}`,
					label: a.name
				}))
		}))
	);

	function teamBySlug(slug: string): Schemas['Team'] | undefined {
		return teams.find((tm) => tm.slug === slug);
	}
</script>

<Tree {nodes} bind:expanded class={className}>
	{#snippet row(node)}
		{#if node.id.startsWith('agent:')}
			{@const agentId = node.id.slice('agent:'.length)}
			<a href="/agents/{agentId}" class="truncate text-sm text-muted no-underline hover:text-foreground">
				{node.label}
			</a>
		{:else}
			{@const tm = teamBySlug(node.id)}
			<button
				type="button"
				onclick={(ev) => {
					ev.stopPropagation();
					onSelectTeam?.(selectedTeam === node.id ? '' : node.id);
				}}
				class="flex min-w-0 flex-1 cursor-pointer items-center justify-between gap-2 text-left {selectedTeam ===
				node.id
					? 'font-semibold text-foreground'
					: 'text-foreground'}"
			>
				<span class="truncate">{tm?.name ?? node.id}</span>
				<span class="font-mono text-xs text-muted tabular-nums">
					{tm?.agentCount ?? 0}
				</span>
			</button>
		{/if}
	{/snippet}
</Tree>
