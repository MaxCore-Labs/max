<script lang="ts">
	import Terminal from '@lucide/svelte/icons/terminal';

	interface Props {
		label: string;
		class?: string;
	}

	let { label, class: className = '' }: Props = $props();
</script>

<span
	class="mt-2 inline-flex h-7 max-w-full items-center gap-1.5 rounded-[var(--radius-sm)] bg-background px-2 text-xs text-muted {className}"
>
	<Terminal size={14} strokeWidth={2} aria-hidden="true" class="shrink-0" />
	<span class="truncate">{label}</span>
</span>
