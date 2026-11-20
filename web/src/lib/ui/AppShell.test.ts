import { render } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { describe, expect, it } from 'vitest';
import AppShell from './AppShell.svelte';

const bodySnippet = createRawSnippet(() => ({
	render: () => `<div>Nội dung</div>`
}));

describe('AppShell navigation', () => {
	it('hides count when inboxCount=0, renders 99+ when inboxCount=120, and marks current with aria-current="page"', () => {
		const { container: c0 } = render(AppShell, {
			props: {
				inboxCount: 0,
				current: '/',
				children: bodySnippet
			}
		});
		expect(c0.querySelector('[data-inbox-badge]')).toBeNull();
		const active0 = c0.querySelector('a[href="/"][aria-current="page"]');
		expect(active0).not.toBeNull();

		const { container: c120 } = render(AppShell, {
			props: {
				inboxCount: 120,
				current: '/inbox',
				children: bodySnippet
			}
		});
		const badges = c120.querySelectorAll('[data-inbox-badge]');
		expect(badges.length).toBeGreaterThan(0);
		expect(badges[0]?.textContent?.trim()).toBe('99+');
		expect(c120.querySelector('a[href="/inbox"][aria-current="page"]')).not.toBeNull();
	});
});
