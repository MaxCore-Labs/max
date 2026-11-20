import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import StatusBadge from './StatusBadge.svelte';

describe('StatusBadge', () => {
	it('always renders visible label text and marks icon aria-hidden', () => {
		const { container } = render(StatusBadge, {
			props: { kind: 'run', status: 'running' }
		});
		const badge = container.firstElementChild as HTMLElement;
		expect(badge).not.toBeNull();
		expect(badge.textContent?.trim().length).toBeGreaterThan(0);

		const svg = badge.querySelector('svg');
		expect(svg).not.toBeNull();
		expect(svg?.getAttribute('aria-hidden')).toBe('true');
	});
});
