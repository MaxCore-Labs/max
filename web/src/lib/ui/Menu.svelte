<script lang="ts">
	import { t } from '#lib/i18n/vi.js';
	import type { Component, Snippet } from 'svelte';

	export interface MenuItem {
		labelKey: string;
		icon?: Component<{ size?: number; strokeWidth?: number; class?: string; 'aria-hidden'?: boolean | 'true' | 'false' }>;
		danger?: boolean;
		onselect: () => void;
	}

	interface Props {
		items: MenuItem[];
		ariaLabel?: string;
		class?: string;
		trigger: Snippet;
	}

	let { items, ariaLabel, class: className = '', trigger }: Props = $props();

	const uid = $props.id();
	let triggerEl: HTMLButtonElement | undefined = $state();
	let popoverEl: HTMLDivElement | undefined = $state();
	let isOpen = $state(false);
	let activeIndex = $state(0);

	function placePopover(): void {
		if (!triggerEl || !popoverEl || typeof window === 'undefined') return;
		const margin = 8;
		const gap = 6;
		const r = triggerEl.getBoundingClientRect();
		const pr = popoverEl.getBoundingClientRect();
		const pw = Math.ceil(pr.width) || 208;
		const ph = Math.ceil(pr.height) || 120;
		const maxL = Math.max(margin, window.innerWidth - pw - margin);
		const left = Math.min(Math.max(margin, Math.round(r.left)), maxL);
		const maxT = Math.max(margin, window.innerHeight - ph - margin);
		let top = Math.round(r.bottom + gap);
		if (top > maxT) {
			const flipped = Math.round(r.top - gap - ph);
			top = flipped >= margin ? Math.min(flipped, maxT) : maxT;
		}
		popoverEl.style.inset = 'auto';
		popoverEl.style.position = 'fixed';
		popoverEl.style.margin = '0';
		popoverEl.style.left = `${left}px`;
		popoverEl.style.top = `${top}px`;
	}

	function toggleMenu(): void {
		if (!popoverEl) return;
		if (isOpen) {
			popoverEl.hidePopover();
		} else {
			activeIndex = 0;
			popoverEl.showPopover();
		}
	}

	function handleToggle(ev: Event): void {
		const next = (ev as ToggleEvent).newState === 'open';
		isOpen = next;
		if (next) {
			placePopover();
			requestAnimationFrame(placePopover);
		}
	}

	function handleKeyDown(ev: KeyboardEvent): void {
		if (items.length === 0) return;
		if (ev.key === 'ArrowDown') {
			ev.preventDefault();
			activeIndex = (activeIndex + 1) % items.length;
		} else if (ev.key === 'ArrowUp') {
			ev.preventDefault();
			activeIndex = (activeIndex - 1 + items.length) % items.length;
		} else if (ev.key === 'Enter' || ev.key === ' ') {
			ev.preventDefault();
			const picked = items[activeIndex];
			if (picked) selectItem(picked);
		} else if (ev.key === 'Escape') {
			popoverEl?.hidePopover();
		}
	}

	function selectItem(item: MenuItem): void {
		item.onselect();
		popoverEl?.hidePopover();
	}

	$effect(() => {
		if (!isOpen || typeof window === 'undefined') return;
		const onScroll = () => {
			if (!triggerEl || !popoverEl) return;
			const r = triggerEl.getBoundingClientRect();
			if (r.bottom < 0 || r.top > window.innerHeight) popoverEl.hidePopover();
			else placePopover();
		};
		window.addEventListener('resize', placePopover);
		window.addEventListener('scroll', onScroll, true);
		return () => {
			window.removeEventListener('resize', placePopover);
			window.removeEventListener('scroll', onScroll, true);
		};
	});
</script>

<div class="inline-block {className}">
	<button
		bind:this={triggerEl}
		type="button"
		data-menu-trigger
		aria-label={ariaLabel}
		aria-haspopup="menu"
		aria-expanded={isOpen}
		aria-controls="{uid}-menu"
		onclick={toggleMenu}
		class="inline-flex min-h-[var(--control-h-dense)] cursor-pointer items-center gap-1.5 rounded-[var(--radius-sm)] border border-border-strong bg-surface px-2.5 text-sm font-medium text-foreground transition-colors hover:bg-button-hover"
	>
		{@render trigger()}
	</button>

	<div
		bind:this={popoverEl}
		id="{uid}-menu"
		role="menu"
		tabindex="-1"
		data-menu-popover
		popover="auto"
		ontoggle={handleToggle}
		onkeydown={handleKeyDown}
		class="min-w-48 rounded-[var(--radius-lg)] border border-border bg-surface-overlay p-1.5 text-foreground shadow-popover outline-none"
	>
		{#each items as item, idx (item.labelKey)}
			<button
				type="button"
				role="menuitem"
				data-active={idx === activeIndex ? 'true' : 'false'}
				onmouseenter={() => (activeIndex = idx)}
				onclick={() => selectItem(item)}
				class="flex h-10 w-full cursor-pointer items-center gap-2 rounded-[var(--radius)] px-3 text-left text-sm text-foreground transition-colors {item.danger
					? 'hover:bg-danger-bg hover:text-danger'
					: idx === activeIndex
						? 'bg-item-hover'
						: 'hover:bg-item-hover'}"
			>
				{#if item.icon}
					<item.icon size={16} strokeWidth={2} aria-hidden="true" class="shrink-0" />
				{/if}
				<span>{t(item.labelKey)}</span>
			</button>
		{/each}
	</div>
</div>
