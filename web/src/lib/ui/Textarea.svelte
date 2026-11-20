<script lang="ts">
	import { getFieldContext } from './Field.svelte';

	interface Props {
		value?: string;
		id?: string;
		name?: string;
		rows?: number;
		placeholder?: string;
		invalid?: boolean;
		disabled?: boolean;
		class?: string;
		oninput?: (ev: Event) => void;
		onkeydown?: (ev: KeyboardEvent) => void;
	}

	let {
		value = $bindable(''), id, name, rows = 3, placeholder,
		invalid = false, disabled = false, class: className = '', oninput, onkeydown
	}: Props = $props();

	const field = getFieldContext();
	let resolvedId = $derived(id ?? field?.id);
	let isInvalid = $derived(invalid || Boolean(field?.hasError));
	let descBy = $derived(field?.hasMessage ? field.messageId : undefined);
</script>

<textarea
	id={resolvedId} {name} {rows} {placeholder} {disabled}
	bind:value
	aria-invalid={isInvalid ? 'true' : undefined}
	aria-describedby={descBy}
	{oninput} {onkeydown}
	class="min-h-22 w-full resize-y rounded-[var(--radius)] border bg-surface px-4 py-2.5 text-base text-foreground transition-colors outline-none placeholder:text-muted focus:ring-2 disabled:cursor-not-allowed disabled:opacity-50 md:text-sm {isInvalid ? 'border-error focus:ring-error/15' : 'border-border-strong focus:border-primary focus:ring-primary/10'} {className}"
></textarea>
