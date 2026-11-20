import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import EnginePicker from './EnginePicker.svelte';

describe('EnginePicker', () => {
	const enginesMeta = [
		{ name: 'agy', enabled: true, slots: 3, defaultModel: 'default' },
		{ name: 'claude', enabled: true, slots: 2, defaultModel: 'sonnet' },
		{ name: 'codex', enabled: false, slots: 2, defaultModel: 'default' }
	];

	it('lists only enabled engines, reorders priority, and blocks deselecting the last engine', async () => {
		const onsave = vi.fn();
		render(EnginePicker, {
			props: {
				availableEngines: enginesMeta,
				selectedEngines: ['agy', 'claude'],
				models: { agy: 'default', claude: 'sonnet' },
				sensitive: false,
				allowUngatedEngine: false,
				onsave
			}
		});

		expect(screen.queryByTestId('engine-opt-codex')).toBeNull();
		expect(screen.getByTestId('engine-opt-agy')).toBeInTheDocument();
		expect(screen.getByTestId('engine-opt-claude')).toBeInTheDocument();

		// Reorder: move second engine ('claude') up to first position
		await fireEvent.click(screen.getByRole('button', { name: 'Tăng ưu tiên claude' }));
		await fireEvent.click(screen.getByRole('button', { name: 'Lưu' }));
		expect(onsave).toHaveBeenCalledWith({
			engines: ['claude', 'agy'],
			models: { agy: 'default', claude: 'sonnet' },
			allowUngatedEngine: false
		});

		// Deselect one engine -> allowed; deselect last remaining engine -> blocked
		await fireEvent.click(screen.getByTestId('engine-check-agy'));
		const lastCheckbox = screen.getByTestId('engine-check-claude') as HTMLInputElement;
		expect(lastCheckbox.disabled).toBe(true);
	});

	it('warns and requires confirmation before sending allowUngatedEngine on level 2/3 engine for sensitive role', async () => {
		const onsave = vi.fn();
		render(EnginePicker, {
			props: {
				availableEngines: enginesMeta,
				selectedEngines: ['claude'],
				models: { claude: 'sonnet' },
				sensitive: true,
				allowUngatedEngine: false,
				onsave
			}
		});

		// Select level-3 'agy' on a sensitive role and try saving
		await fireEvent.click(screen.getByTestId('engine-check-agy'));
		await fireEvent.click(screen.getByRole('button', { name: 'Lưu' }));

		// Must NOT save yet; warning dialog must be open
		expect(onsave).not.toHaveBeenCalled();
		const confirmBtn = screen.getByRole('button', { name: 'Tôi hiểu rủi ro, mở khoá' });
		expect(confirmBtn).toBeInTheDocument();

		await fireEvent.click(confirmBtn);
		expect(onsave).toHaveBeenCalledWith(
			expect.objectContaining({
				engines: ['claude', 'agy'],
				allowUngatedEngine: true
			})
		);
	});
});
