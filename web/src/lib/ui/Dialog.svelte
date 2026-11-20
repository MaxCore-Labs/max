<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		open?: boolean;
		title: string;
		busy?: boolean;
		class?: string;
		children?: Snippet;
		actions?: Snippet;
	}

	let {
		open = $bindable(false),
		title,
		busy = false,
		class: className = '',
		children,
		actions
	}: Props = $props();

	const uid = $props.id();

	function syncDialog(el: HTMLDialogElement) {
		$effect(() => {
			if (open) {
				if (!el.open) el.showModal();
			} else if (el.open) {
				el.close();
			}
		});
	}

	function handleCancel(ev: Event): void {
		if (busy) {
			ev.preventDefault();
			return;
		}
		open = false;
	}

	function handleBackdropClick(ev: MouseEvent): void {
		if (!busy && ev.target === ev.currentTarget) {
			open = false;
		}
	}
</script>

<dialog
	{@attach syncDialog}
	aria-labelledby="{uid}-title"
	oncancel={handleCancel}
	onclick={handleBackdropClick}
	class="m-auto w-full max-w-md rounded-[var(--radius-lg)] border border-border bg-surface p-0 text-foreground shadow-modal backdrop:bg-foreground/30 {className}"
>
	<div class="p-6">
		<h3 id="{uid}-title" class="text-base font-semibold text-foreground">{title}</h3>
		{#if children}
			<div class="mt-2 text-sm text-muted">{@render children()}</div>
		{/if}
		{#if actions}
			<div class="mt-6 flex flex-wrap justify-end gap-2">{@render actions()}</div>
		{/if}
	</div>
</dialog>
