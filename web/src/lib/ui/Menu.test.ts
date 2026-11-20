import { fireEvent, render } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import Menu from './Menu.svelte';

describe('Menu popover primitive', () => {
	it('navigates items with ArrowDown/ArrowUp, invokes onselect then closes, and keeps danger un-red when not hovered', async () => {
		const onFirst = vi.fn();
		const onDanger = vi.fn();
		const trigger = createRawSnippet(() => ({
			render: () => `<span>Mở menu</span>`
		}));

		const { container, getByText } = render(Menu, {
			props: {
				trigger,
				items: [
					{ labelKey: 'Xem chi tiết', onselect: onFirst },
					{ labelKey: 'Thu hồi quyền', danger: true, onselect: onDanger }
				]
			}
		});

		const triggerBtn = container.querySelector('[data-menu-trigger]') as HTMLButtonElement;
		await fireEvent.click(triggerBtn);

		const menuEl = container.querySelector('[data-menu-popover]') as HTMLElement;
		expect(menuEl.hasAttribute('data-popover-open')).toBe(true);

		const dangerBtn = getByText('Thu hồi quyền').closest('button') as HTMLButtonElement;
		const nonHoverClasses = dangerBtn.className.split(/\s+/).filter((c) => !c.startsWith('hover:'));
		expect(nonHoverClasses).not.toContain('text-danger');
		expect(nonHoverClasses).not.toContain('bg-danger-bg');

		await fireEvent.keyDown(menuEl, { key: 'ArrowDown' });
		expect(dangerBtn.getAttribute('data-active')).toBe('true');

		await fireEvent.keyDown(menuEl, { key: 'ArrowUp' });
		const firstBtn = getByText('Xem chi tiết').closest('button') as HTMLButtonElement;
		expect(firstBtn.getAttribute('data-active')).toBe('true');

		await fireEvent.click(firstBtn);
		expect(onFirst).toHaveBeenCalledTimes(1);
		expect(menuEl.hasAttribute('data-popover-open')).toBe(false);
	});
});
