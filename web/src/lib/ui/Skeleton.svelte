<script lang="ts">
	interface Props {
		lines?: number;
		class?: string;
	}

	const WIDTHS = ['w-3/4', 'w-11/12', 'w-3/5', 'w-4/5'];
	let { lines = 3, class: className = '' }: Props = $props();
</script>

<div aria-busy="true" class="flex flex-col gap-3 py-4 {className}">
	{#each Array.from({ length: lines }, (_, i) => i) as idx (idx)}
		<div
			class="h-3 animate-pulse rounded-[var(--radius-full)] bg-background-hover motion-reduce:animate-none {WIDTHS[
				idx % WIDTHS.length
			]}"
		></div>
	{/each}
</div>
