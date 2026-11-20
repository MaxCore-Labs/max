import { fireEvent, render } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '#lib/api/client.js';
import EngineSettings from './EngineSettings.svelte';

describe('EngineSettings validation and login guard', () => {
	afterEach(() => {
		vi.restoreAllMocks();
	});

	it('blocks slot values outside 1-8, prevents disabling the last enabled engine, and locks unauthenticated engines with login command', async () => {
		const saveSpy = vi.spyOn(api, 'updateSettings').mockResolvedValue({
			defaultEngine: 'agy',
			engines: [{ name: 'agy', enabled: true, slots: 3, defaultModel: 'default' }]
		});

		const { getByLabelText, getByTestId, getByText } = render(EngineSettings, {
			props: {
				defaultEngine: 'agy',
				settings: [
					{ name: 'agy', enabled: true, slots: 3, defaultModel: 'default' },
					{ name: 'codex', enabled: false, slots: 2, defaultModel: 'default' }
				],
				statuses: [
					{ name: 'agy', enabled: true, installed: true, loggedIn: true, slots: 3, used: 1 },
					{ name: 'codex', enabled: false, installed: true, loggedIn: false, slots: 2, used: 0 }
				]
			}
		});

		// 1. Unauthenticated engine shows login command and locks enable toggle
		expect(getByText(/codex login/)).toBeTruthy();
		const codexToggle = getByTestId('engine-toggle-codex') as HTMLButtonElement;
		expect(codexToggle.disabled).toBe(true);

		// 2. Last enabled engine cannot be disabled
		const agyToggle = getByTestId('engine-toggle-agy') as HTMLButtonElement;
		expect(agyToggle.disabled).toBe(true);

		// 3. Slots outside 1-8 rejected before calling API
		const agySlots = getByLabelText(/Số chỗ song song · agy/) as HTMLInputElement;
		await fireEvent.input(agySlots, { target: { value: '9' } });
		const saveBtn = getByText('Lưu').closest('button')!;
		await fireEvent.click(saveBtn);

		expect(getByText('Số chỗ phải từ 1 đến 8.')).toBeTruthy();
		expect(saveSpy).not.toHaveBeenCalled();
	});
});
