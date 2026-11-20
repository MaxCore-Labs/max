<script lang="ts">
	import { t } from '#lib/i18n/vi.js';
	import Activity from '@lucide/svelte/icons/activity';
	import BookOpen from '@lucide/svelte/icons/book-open';
	import Bot from '@lucide/svelte/icons/bot';
	import Gauge from '@lucide/svelte/icons/gauge';
	import Inbox from '@lucide/svelte/icons/inbox';
	import ListChecks from '@lucide/svelte/icons/list-checks';
	import MenuIcon from '@lucide/svelte/icons/menu';
	import MessageSquare from '@lucide/svelte/icons/message-square';
	import Settings from '@lucide/svelte/icons/settings';
	import type { Component, Snippet } from 'svelte';

	export interface NavItem {
		href: string;
		labelKey: string;
		icon: Component<{ size?: number; strokeWidth?: number; class?: string; 'aria-hidden'?: boolean | 'true' | 'false' }>;
		isInbox?: boolean;
	}

	const DEFAULT_ITEMS: NavItem[] = [
		{ href: '/', labelKey: 'nav.chat', icon: MessageSquare },
		{ href: '/inbox', labelKey: 'nav.inbox', icon: Inbox, isInbox: true },
		{ href: '/agents', labelKey: 'nav.agents', icon: Bot },
		{ href: '/tasks', labelKey: 'nav.tasks', icon: ListChecks },
		{ href: '/activity', labelKey: 'nav.activity', icon: Activity },
		{ href: '/knowledge', labelKey: 'nav.knowledge', icon: BookOpen },
		{ href: '/usage', labelKey: 'nav.usage', icon: Gauge },
		{ href: '/system', labelKey: 'nav.system', icon: Settings }
	];

	interface Props {
		items?: NavItem[];
		inboxCount?: number;
		current: string;
		class?: string;
		topbar?: Snippet;
		children: Snippet;
	}

	let {
		items = DEFAULT_ITEMS,
		inboxCount = 0,
		current,
		class: className = '',
		topbar,
		children
	}: Props = $props();

	let badgeText = $derived(inboxCount <= 0 ? '' : inboxCount > 99 ? '99+' : String(inboxCount));

	function isActive(href: string): boolean {
		return href === '/' ? current === '/' : current === href || current.startsWith(`${href}/`);
	}
</script>

<div class="flex min-h-screen bg-background text-foreground {className}">
	<aside
		aria-label={t('nav.main')}
		class="hidden w-60 shrink-0 flex-col gap-0.5 border-r border-border-strong bg-surface p-2 lg:flex"
	>
		<div class="px-3 py-2 text-base font-semibold text-foreground">{t('app.title')}</div>
		<nav class="flex flex-col gap-0.5" aria-label={t('nav.main')}>
			{#each items as item (item.href)}
				{@const active = isActive(item.href)}
				<a
					href={item.href}
					aria-current={active ? 'page' : undefined}
					class="flex h-10 items-center gap-3 rounded-[var(--radius)] px-3 text-sm font-medium no-underline transition-colors {active
						? 'bg-secondary text-foreground'
						: 'text-muted hover:bg-item-hover hover:text-foreground'}"
				>
					<item.icon size={16} strokeWidth={2} aria-hidden="true" class="shrink-0" />
					<span>{t(item.labelKey)}</span>
					{#if item.isInbox && badgeText}
						<span
							data-inbox-badge
							class="ml-auto h-5 min-w-5 rounded-[var(--radius-full)] bg-primary px-1.5 text-center text-xs leading-5 text-primary-foreground tabular-nums"
						>
							{badgeText}
						</span>
					{/if}
				</a>
			{/each}
		</nav>
	</aside>

	<div class="flex min-w-0 flex-1 flex-col pb-16 md:pb-0">
		{#if topbar}
			<header
				class="flex min-h-14 flex-wrap items-center justify-between gap-3 border-b border-border-strong bg-surface px-4 py-2 sm:px-6"
			>
				{@render topbar()}
			</header>
		{/if}
		<main class="min-w-0 flex-1 p-4 sm:p-6">{@render children()}</main>
	</div>

	<nav
		aria-label={t('nav.mobile')}
		class="fixed right-0 bottom-0 left-0 z-30 grid grid-cols-4 border-t border-border-strong bg-surface p-1 md:hidden"
	>
		{#each [items[0], items[1], items[3], { href: '/system', labelKey: 'nav.more', icon: MenuIcon, isInbox: false }] as tab (tab?.href)}
			{#if tab}
				{@const active = isActive(tab.href)}
				<a
					href={tab.href}
					aria-current={active ? 'page' : undefined}
					class="relative flex h-13 flex-col items-center justify-center gap-0.5 rounded-[var(--radius)] text-xs font-medium no-underline {active
						? 'text-foreground'
						: 'text-muted'}"
				>
					<tab.icon size={20} strokeWidth={2} aria-hidden="true" />
					<span>{t(tab.labelKey)}</span>
					{#if tab.isInbox && badgeText}
						<span
							data-inbox-badge
							class="absolute top-1.5 left-1/2 ml-1.5 h-4 min-w-4 rounded-[var(--radius-full)] bg-primary px-1 text-center text-[10px] leading-4 text-primary-foreground tabular-nums"
						>
							{badgeText}
						</span>
					{/if}
				</a>
			{/if}
		{/each}
	</nav>
</div>
