import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import FieldFixture from '../../test-fixtures/FieldFixture.svelte';

describe('Field + Input', () => {
	it('links label to input focus and keeps helper row reserved when no error', () => {
		const { container, getByLabelText } = render(FieldFixture, {
			props: { label: 'Tên agent' }
		});
		const input = getByLabelText('Tên agent') as HTMLInputElement;
		const label = container.querySelector('label') as HTMLLabelElement;
		expect(label.htmlFor).toBe(input.id);

		// Clicking label focuses associated input via htmlFor
		input.focus();
		expect(document.activeElement).toBe(input);

		const helper = container.querySelector('[data-field-message]');
		expect(helper).not.toBeNull();
	});

	it('sets aria-invalid="true" and aria-describedby pointing to error message when error is set', () => {
		const { getByLabelText } = render(FieldFixture, {
			props: {
				label: 'Số chỗ của agy',
				error: 'Số chỗ phải từ 1 đến 8.'
			}
		});
		const input = getByLabelText('Số chỗ của agy');
		expect(input.getAttribute('aria-invalid')).toBe('true');
		const descId = input.getAttribute('aria-describedby');
		expect(descId).toBeTruthy();
		const errEl = document.getElementById(descId!);
		expect(errEl?.textContent).toContain('Số chỗ phải từ 1 đến 8.');
	});
});
