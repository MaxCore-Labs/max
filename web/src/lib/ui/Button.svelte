<script lang="ts">
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import type { Component, Snippet } from 'svelte';

	export type ButtonVariant = 'outline' | 'primary' | 'secondary' | 'ghost' | 'danger';

	interface Props {
		variant?: ButtonVariant;
		icon?: Component<{ size?: number; strokeWidth?: number; class?: string; 'aria-hidden'?: boolean | 'true' | 'false' }>;
		loading?: boolean;
		disabled?: boolean;
		iconOnly?: boolean;
		dense?: boolean;
		ariaLabel?: string;
		type?: 'button' | 'submit' | 'reset';
		class?: string;
		onclick?: (ev: MouseEvent) => void;
		children?: Snippet;
	}

	let {
		variant = 'outline', icon: Icon, loading = false, disabled = false,
		iconOnly = false, dense = false, ariaLabel, type = 'button',
		class: className = '', onclick, children
	}: Props = $props();

	let resolvedAriaLabel = $derived.by(() => {
		if (iconOnly && !ariaLabel) throw new Error('Button with iconOnly requires ariaLabel');
		return ariaLabel;
	});

	const VARIANTS: Record<ButtonVariant, string> = {
		outline: 'border-border-strong bg-surface text-foreground hover:bg-button-hover',
		primary: 'border-transparent bg-primary text-primary-foreground hover:bg-primary-hover',
		secondary: 'border-transparent bg-secondary text-foreground hover:bg-secondary-hover',
		ghost: 'border-transparent bg-transparent text-muted hover:bg-item-hover hover:text-foreground',
		danger: 'border-transparent bg-danger-bg text-danger hover:opacity-90'
	};

	const sizeCls = $derived(
		dense
			? 'min-h-[var(--control-h-dense)] w-[var(--control-h-dense)] rounded-[var(--radius-sm)] p-0'
			: iconOnly
				? 'min-h-[var(--control-h-touch)] w-[var(--control-h-touch)] rounded-[var(--radius)] p-0 md:min-h-[var(--control-h)] md:w-[var(--control-h)]'
				: 'min-h-[var(--control-h-touch)] rounded-[var(--radius)] px-4 py-2 md:min-h-[var(--control-h)]'
	);
</script>

<button
	{type}
	disabled={disabled ? true : undefined}
	aria-busy={loading ? 'true' : undefined}
	aria-disabled={loading ? 'true' : undefined}
	aria-label={resolvedAriaLabel}
	onclick={(ev) => (loading || disabled ? ev.preventDefault() : onclick?.(ev))}
	class="inline-flex max-w-full cursor-pointer items-center justify-center gap-2 border text-center text-sm leading-tight font-medium transition-colors outline-none disabled:cursor-not-allowed disabled:opacity-50 aria-busy:cursor-progress {sizeCls} {VARIANTS[variant]} {className}"
>
	{#if loading}
		<LoaderCircle size={16} strokeWidth={2} aria-hidden="true" class="shrink-0 animate-spin motion-reduce:animate-none" />
	{:else if Icon}
		<Icon size={16} strokeWidth={2} aria-hidden="true" class="shrink-0" />
	{/if}
	{#if !iconOnly && !dense && children}{@render children()}{/if}
</button>
