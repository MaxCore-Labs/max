import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import QuotaMeter from './QuotaMeter.svelte';

describe('QuotaMeter states', () => {
	it('displays "Đang học" without percentage when ceiling is unknown', () => {
		const { container, getByText } = render(QuotaMeter, {
			props: {
				quota: {
					engine: 'agy',
					usedShort: 1204880,
					usedWeek: 1204880,
					estShort: null,
					estWeek: null,
					limitedUntil: null
				}
			}
		});

		expect(getByText('Đang học')).toBeTruthy();
		expect(container.textContent).not.toContain('%');
	});

	it('displays reopen time and queued count when limitedUntil is active', () => {
		const future = Date.now() + 2 * 3_600_000;
		const { getByText } = render(QuotaMeter, {
			props: {
				quota: {
					engine: 'agy',
					usedShort: 900000,
					usedWeek: 2000000,
					estShort: 900000,
					estWeek: null,
					limitedUntil: future
				},
				queuedCount: 14
			}
		});

		expect(getByText(/Mở lại \d{2}:\d{2}/)).toBeTruthy();
		expect(getByText(/14 việc đang chờ/)).toBeTruthy();
	});
});
