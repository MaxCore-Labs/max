<script lang="ts">
	import Dialog from '#lib/ui/Dialog.svelte';

	interface Props {
		initialOpen?: boolean;
		busy?: boolean;
	}

	let { initialOpen = true, busy = false }: Props = $props();
	let open = $state(false);
	$effect.pre(() => {
		open = initialOpen;
	});
</script>

<span data-testid="open-state">{String(open)}</span>
<Dialog bind:open title="Xoá agent?" {busy}>
	<p>Nội dung hộp thoại.</p>
</Dialog>
