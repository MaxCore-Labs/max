<script lang="ts">
	import { getFieldContext } from './Field.svelte';

	interface Props {
		value?: string | number;
		type?: 'text' | 'password' | 'number' | 'search' | 'email' | 'url';
		id?: string;
		name?: string;
		placeholder?: string;
		invalid?: boolean;
		disabled?: boolean;
		readonly?: boolean;
		autocomplete?: AutoFill;
		min?: number;
		max?: number;
		class?: string;
		oninput?: (ev: Event) => void;
		onchange?: (ev: Event) => void;
		onkeydown?: (ev: KeyboardEvent) => void;
	}

	let {
		value = $bindable(''), type = 'text', id, name, placeholder, invalid = false,
		disabled = false, readonly = false, autocomplete, min, max, class: className = '',
		oninput, onchange, onkeydown
	}: Props = $props();

	const field = getFieldContext();
	let resolvedId = $derived(id ?? field?.id);
	let isInvalid = $derived(invalid || Boolean(field?.hasError));
	let descBy = $derived(field?.hasMessage ? field.messageId : undefined);
</script>

<input
	id={resolvedId} {name} {type} {placeholder} {disabled} {readonly} {autocomplete} {min} {max}
	bind:value
	aria-invalid={isInvalid ? 'true' : undefined}
	aria-describedby={descBy}
	{oninput} {onchange} {onkeydown}
	class="h-[var(--control-h-touch)] w-full rounded-[var(--radius)] border bg-surface px-4 text-base text-foreground transition-colors outline-none placeholder:text-muted focus:ring-2 disabled:cursor-not-allowed disabled:opacity-50 md:h-[var(--control-h)] md:text-sm {isInvalid ? 'border-error focus:ring-error/15' : 'border-border-strong focus:border-primary focus:ring-primary/10'} {className}"
/>
