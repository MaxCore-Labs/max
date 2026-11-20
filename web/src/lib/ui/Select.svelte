<script lang="ts">
	import { getFieldContext } from './Field.svelte';

	export interface SelectOption { value: string; label: string; disabled?: boolean }

	interface Props {
		value?: string;
		options: SelectOption[];
		id?: string;
		name?: string;
		invalid?: boolean;
		disabled?: boolean;
		class?: string;
		onchange?: (ev: Event) => void;
	}

	let { value = $bindable(''), options, id, name, invalid = false, disabled = false, class: className = '', onchange }: Props = $props();

	const field = getFieldContext();
	let resolvedId = $derived(id ?? field?.id);
	let isInvalid = $derived(invalid || Boolean(field?.hasError));
	let descBy = $derived(field?.hasMessage ? field.messageId : undefined);
</script>

<select
	id={resolvedId} {name} {disabled} bind:value
	aria-invalid={isInvalid ? 'true' : undefined}
	aria-describedby={descBy}
	{onchange}
	class="h-[var(--control-h-touch)] w-full rounded-[var(--radius)] border bg-surface px-3 text-base text-foreground transition-colors outline-none focus:ring-2 disabled:cursor-not-allowed disabled:opacity-50 md:h-[var(--control-h)] md:text-sm {isInvalid ? 'border-error focus:ring-error/15' : 'border-border-strong focus:border-primary focus:ring-primary/10'} {className}"
>
	{#each options as opt (opt.value)}
		<option value={opt.value} disabled={opt.disabled}>{opt.label}</option>
	{/each}
</select>
