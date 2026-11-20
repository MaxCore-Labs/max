import { fireEvent, render } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import DataTable from './DataTable.svelte';

interface SampleRow {
	id: number;
	name: string;
	tokens: number;
}

describe('DataTable fixed-row virtualization', () => {
	it('renders < 60 <tr> elements for 10,000 rows, applies tabular-nums to right-aligned columns, and calls onEndReached once per bottom scroll', async () => {
		const rows: SampleRow[] = Array.from({ length: 10000 }, (_, i) => ({
			id: i + 1,
			name: `Agent ${i + 1}`,
			tokens: (i + 1) * 100
		}));
		const onEndReached = vi.fn();

		const { container } = render(DataTable<SampleRow>, {
			props: {
				rows,
				rowKey: (r) => r.id,
				onEndReached,
				columns: [
					{ key: 'name', labelKey: 'Agent' },
					{ key: 'tokens', labelKey: 'Token hôm nay', align: 'right' }
				]
			}
		});

		const trs = container.querySelectorAll('tr');
		expect(trs.length).toBeLessThan(60);

		const rightCell = container.querySelector('td.tabular-nums');
		expect(rightCell).not.toBeNull();

		const scroller = container.querySelector('[data-table-scroller]') as HTMLElement;
		expect(scroller).not.toBeNull();

		Object.defineProperty(scroller, 'clientHeight', { value: 400, configurable: true });
		Object.defineProperty(scroller, 'scrollHeight', { value: 400000, configurable: true });

		scroller.scrollTop = 399650;
		await fireEvent.scroll(scroller);
		await fireEvent.scroll(scroller);
		expect(onEndReached).toHaveBeenCalledTimes(1);

		// Scroll up and back to bottom -> triggers a second time
		scroller.scrollTop = 1000;
		await fireEvent.scroll(scroller);
		scroller.scrollTop = 399650;
		await fireEvent.scroll(scroller);
		expect(onEndReached).toHaveBeenCalledTimes(2);
	});
});
