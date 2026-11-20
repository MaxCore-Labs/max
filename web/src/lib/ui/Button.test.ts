import Plus from '@lucide/svelte/icons/plus';
import { fireEvent, render } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import Button from './Button.svelte';

const textSnippet = (txt: string) =>
	createRawSnippet(() => ({
		render: () => `<span>${txt}</span>`
	}));

describe('Button', () => {
	it('sets aria-busy="true", omits disabled attribute, and suppresses onclick while loading', async () => {
		const onclick = vi.fn();
		const { getByRole } = render(Button, {
			props: {
				variant: 'primary',
				loading: true,
				onclick,
				children: textSnippet('Cho phép')
			}
		});

		const btn = getByRole('button');
		expect(btn.getAttribute('aria-busy')).toBe('true');
		expect(btn.hasAttribute('disabled')).toBe(false);

		await fireEvent.click(btn);
		expect(onclick).not.toHaveBeenCalled();
	});

	it('throws in dev when iconOnly is true without ariaLabel', () => {
		expect(() =>
			render(Button, {
				props: {
					icon: Plus,
					iconOnly: true
				}
			})
		).toThrow(/ariaLabel/i);
	});
});
