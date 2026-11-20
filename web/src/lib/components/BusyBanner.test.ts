import { fireEvent, render } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '#lib/api/client.js';
import BusyBanner from './BusyBanner.svelte';

describe('BusyBanner', () => {
	afterEach(() => {
		vi.restoreAllMocks();
	});

	it('displays task title and elapsed minutes, and calls cancelRun with mode=requeue on stop', async () => {
		const cancelSpy = vi.spyOn(api, 'cancelRun').mockResolvedValue({
			id: 'run-busy',
			agentId: 'ag-1',
			conversationId: 'c-1',
			status: 'cancelled',
			priority: 50,
			engine: 'agy',
			model: 'default',
			turns: 2,
			inputTokens: 100,
			outputTokens: 50,
			startedAt: 1000
		});

		const { getByText } = render(BusyBanner, {
			props: {
				busy: {
					runId: 'run-busy',
					title: 'Tổng hợp báo cáo quý',
					minutesRunning: 7
				}
			}
		});

		expect(getByText(/Tổng hợp báo cáo quý/)).toBeTruthy();
		expect(getByText(/7 phút/)).toBeTruthy();

		const stopBtn = getByText('Dừng việc đó').closest('button')!;
		await fireEvent.click(stopBtn);

		expect(cancelSpy).toHaveBeenCalledWith('run-busy', { mode: 'requeue' });
	});
});
