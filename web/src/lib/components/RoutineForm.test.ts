import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import RoutineForm from './RoutineForm.svelte';

describe('RoutineForm', () => {
	it('shows field error on invalid cron and sets origin="inbound" when external content checkbox is checked', async () => {
		const onsubmit = vi.fn();
		render(RoutineForm, {
			props: {
				agents: [{ slug: 'support-1', name: 'Support 1' }],
				onsubmit
			}
		});

		const slugInput = document.getElementById('routine-slug') as HTMLInputElement;
		const schedInput = document.getElementById('routine-schedule') as HTMLInputElement;
		const bodyInput = document.getElementById('routine-body') as HTMLTextAreaElement;

		await fireEvent.input(slugInput, { target: { value: 'triage-inbox' } });
		await fireEvent.input(schedInput, { target: { value: 'bad cron' } });
		await fireEvent.input(bodyInput, { target: { value: 'Đọc email khách hàng' } });

		await fireEvent.click(screen.getByRole('button', { name: 'Lưu' }));
		expect(onsubmit).not.toHaveBeenCalled();
		expect(schedInput.getAttribute('aria-invalid')).toBe('true');

		// Fix cron and check "Đọc nội dung người ngoài"
		await fireEvent.input(schedInput, { target: { value: '0 8 * * 1-5' } });
		const extCheckbox = screen.getByTestId('routine-inbound-check') as HTMLInputElement;
		await fireEvent.click(extCheckbox);

		await fireEvent.click(screen.getByRole('button', { name: 'Lưu' }));
		expect(onsubmit).toHaveBeenCalledWith(
			expect.objectContaining({
				slug: 'triage-inbox',
				schedule: '0 8 * * 1-5',
				origin: 'inbound'
			})
		);
	});
});
