<script lang="ts">
	import type { Schemas } from '#lib/api/client.js';
	import { StatusBadge, Tree, type TreeNode } from '#lib/ui/index.js';

	interface Props {
		root: Schemas['TaskTreeNode'];
		class?: string;
	}

	let { root, class: className = '' }: Props = $props();

	let expanded = $derived([root.task.id]);

	function mapNode(n: Schemas['TaskTreeNode'], depth: number): TreeNode {
		return {
			id: n.task.id,
			label: n.task.title,
			children: depth < 5 ? n.children.map((ch) => mapNode(ch, depth + 1)) : []
		};
	}

	function findTask(n: Schemas['TaskTreeNode'], id: string): Schemas['Task'] | null {
		if (n.task.id === id) return n.task;
		for (const ch of n.children) {
			const hit = findTask(ch, id);
			if (hit) return hit;
		}
		return null;
	}

	let treeNodes = $derived<TreeNode[]>([mapNode(root, 0)]);
</script>

<Tree nodes={treeNodes} bind:expanded class={className}>
	{#snippet row(node)}
		{@const tk = findTask(root, node.id)}
		<div class="flex min-w-0 flex-1 items-center justify-between gap-2">
			<a href="/tasks/{node.id}" class="truncate text-sm text-foreground no-underline hover:underline">
				{node.label}
			</a>
			{#if tk}
				<StatusBadge kind="task" status={tk.status} reason={tk.blockedReason ?? undefined} />
			{/if}
		</div>
	{/snippet}
</Tree>
