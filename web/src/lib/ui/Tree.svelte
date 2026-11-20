<script lang="ts">
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import type { Snippet } from 'svelte';

	export interface TreeNode {
		id: string;
		label: string;
		children?: TreeNode[];
	}

	interface Props {
		nodes: TreeNode[];
		expanded?: string[];
		row?: Snippet<[TreeNode]>;
		class?: string;
	}

	let { nodes, expanded = $bindable<string[]>([]), row, class: className = '' }: Props = $props();

	function isOpen(id: string): boolean {
		return expanded.includes(id);
	}

	function setOpen(id: string, next: boolean): void {
		if (next && !expanded.includes(id)) {
			expanded = [...expanded, id];
		} else if (!next && expanded.includes(id)) {
			expanded = expanded.filter((x) => x !== id);
		}
	}

	function handleKey(ev: KeyboardEvent, node: TreeNode): void {
		if (!node.children?.length) return;
		if (ev.key === 'ArrowRight') {
			ev.preventDefault();
			setOpen(node.id, true);
		} else if (ev.key === 'ArrowLeft') {
			ev.preventDefault();
			setOpen(node.id, false);
		} else if (ev.key === 'Enter' || ev.key === ' ') {
			ev.preventDefault();
			setOpen(node.id, !isOpen(node.id));
		}
	}
</script>

{#snippet renderLevel(list: TreeNode[], depth: number)}
	{#each list as node (node.id)}
		{@const hasKids = Boolean(node.children?.length)}
		{@const open = hasKids && isOpen(node.id)}
		<div
			role="treeitem"
			tabindex="0"
			aria-selected="false"
			aria-expanded={hasKids ? open : undefined}
			onclick={() => hasKids && setOpen(node.id, !open)}
			onkeydown={(ev) => handleKey(ev, node)}
			class="flex h-10 cursor-pointer items-center gap-2 rounded-[var(--radius)] px-3 text-sm font-medium text-foreground outline-none hover:bg-item-hover {depth >
			0
				? 'ml-5'
				: ''}"
		>
			{#if hasKids}
				{#if open}
					<ChevronDown size={16} strokeWidth={2} aria-hidden="true" class="shrink-0 text-muted" />
				{:else}
					<ChevronRight size={16} strokeWidth={2} aria-hidden="true" class="shrink-0 text-muted" />
				{/if}
			{:else}
				<span class="w-4 shrink-0"></span>
			{/if}
			{#if row}
				{@render row(node)}
			{:else}
				<span class="truncate">{node.label}</span>
			{/if}
		</div>
		{#if open && node.children}
			<div role="group">{@render renderLevel(node.children, depth + 1)}</div>
		{/if}
	{/each}
{/snippet}

<div role="tree" class="flex flex-col gap-0.5 {className}">
	{@render renderLevel(nodes, 0)}
</div>
