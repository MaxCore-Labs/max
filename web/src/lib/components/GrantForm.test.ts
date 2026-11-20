import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import GrantForm from './GrantForm.svelte';

describe('GrantForm', () => {
	it('defaults origins to owner+routine and requires at least one parameter constraint or max_per_day', async () => {
		const onsubmit = vi.fn();
		render(GrantForm, {
			props: {
				onsubmit
			}
		});

		const ownerBox = screen.getByLabelText(/owner/i) as HTMLInputElement;
		const routineBox = screen.getByLabelText(/routine/i) as HTMLInputElement;
		const externalBox = screen.getByLabelText(/external/i) as HTMLInputElement;
		expect(ownerBox.checked).toBe(true);
		expect(routineBox.checked).toBe(true);
		expect(externalBox.checked).toBe(false);

		const connInput = screen.getByLabelText(/Kết nối/i);
		const toolInput = screen.getByLabelText(/Tool/i);
		await fireEvent.input(connInput, { target: { value: 'gmail' } });
		await fireEvent.input(toolInput, { target: { value: 'send_email' } });

		const submitBtn = screen.getByRole('button', { name: /Tạo grant/i });
		await fireEvent.click(submitBtn);
		expect(onsubmit).not.toHaveBeenCalled();
		expect(screen.getByText(/ràng buộc tham số hoặc giới hạn ngày/i)).toBeInTheDocument();

		const maxDayInput = screen.getByLabelText(/Số lần tối đa mỗi ngày/i);
		await fireEvent.input(maxDayInput, { target: { value: '5' } });
		await fireEvent.click(submitBtn);

		expect(onsubmit).toHaveBeenCalledTimes(1);
		expect(onsubmit.mock.calls[0]?.[0]).toMatchObject({
			connection: 'gmail',
			tool: 'send_email',
			maxPerDay: 5,
			allowedRootOrigins: ['owner', 'routine']
		});
	});
});
