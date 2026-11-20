<script lang="ts">
	import Field from '#lib/ui/Field.svelte';
	import Input from '#lib/ui/Input.svelte';

	interface Props {
		label: string;
		hint?: string;
		error?: string;
		value?: string;
	}

	let { label, hint = '', error = '', value = $bindable('') }: Props = $props();
</script>

<Field {label} {hint} {error}>
	<Input bind:value />
</Field>
