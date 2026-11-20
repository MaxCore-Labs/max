<script lang="ts">
	import MarkdownView from './MarkdownView.svelte';
	import ToolChip from './ToolChip.svelte';

	interface Props {
		role: 'user' | 'assistant' | 'system';
		content: string;
		tools?: string[];
		streaming?: boolean;
		class?: string;
	}

	let { role, content, tools = [], streaming = false, class: className = '' }: Props = $props();
	let isOwner = $derived(role === 'user');
</script>

<div class="flex {isOwner ? 'justify-end' : 'justify-start'} {className}">
	<div
		data-streaming={streaming ? 'true' : undefined}
		class="max-w-[min(560px,85%)] rounded-[var(--radius-lg)] px-3.5 py-2.5 text-sm break-words {isOwner
			? 'bg-secondary text-foreground'
			: 'border border-border-strong bg-surface text-foreground'}"
	>
		{#if isOwner}
			<p class="whitespace-pre-wrap">{content}</p>
		{:else}
			<MarkdownView source={content} />
		{/if}
		{#if tools.length > 0}
			<div class="flex flex-wrap gap-1.5">
				{#each tools as toolLabel, idx (`${toolLabel}-${idx}`)}
					<ToolChip label={toolLabel} />
				{/each}
			</div>
		{/if}
	</div>
</div>
