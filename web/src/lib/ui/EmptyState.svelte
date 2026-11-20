<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		message: string;
		class?: string;
		action?: Snippet;
	}

	let { message, class: className = '', action }: Props = $props();
</script>

<p class="py-6 text-center text-sm text-muted {className}">
	{message}
	{#if action}
		<span class="ml-1 inline-block">{@render action()}</span>
	{/if}
</p>
