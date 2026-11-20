<script module lang="ts">
	import { getContext, setContext } from 'svelte';

	export interface FieldContext {
		readonly id: string;
		readonly messageId: string;
		readonly hasError: boolean;
		readonly hasMessage: boolean;
	}
	const FIELD_KEY = Symbol('max-field');
	export const setFieldContext = (ctx: FieldContext) => setContext(FIELD_KEY, ctx);
	export const getFieldContext = () => getContext<FieldContext | undefined>(FIELD_KEY);
</script>

<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		label: string;
		hint?: string;
		error?: string;
		for?: string;
		class?: string;
		children: Snippet;
	}

	let { label, hint = '', error = '', for: forProp, class: className = '', children }: Props = $props();
	const uid = $props.id();
	let fieldId = $derived(forProp || `${uid}-control`);
	let msgId = $derived(`${fieldId}-msg`);

	setFieldContext({
		get id() { return fieldId; },
		get messageId() { return msgId; },
		get hasError() { return Boolean(error); },
		get hasMessage() { return Boolean(error || hint); }
	});
</script>

<div class="flex min-w-0 flex-col gap-1.5 {className}">
	<label for={fieldId} class="w-fit cursor-pointer text-sm font-medium text-foreground">{label}</label>
	{@render children()}
	<p id={msgId} data-field-message class="min-h-4 text-xs leading-4 {error ? 'text-error-text' : 'text-muted'}">
		{error || hint}
	</p>
</div>
