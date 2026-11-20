import { fireEvent, render } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import type { Schemas } from '#lib/api/client.js';
import AgentTable from './AgentTable.svelte';

function makeAgent(i: number): Schemas['Agent'] {
	return {
		id: `ag-${i}`,
		slug: `worker-${String(i).padStart(5, '0')}`,
		name: `Worker ${i}`,
		templateSlug: 'monitor',
		teamId: 'team-core',
		engines: ['agy'],
		models: {},
		allowUngatedEngine: false,
		scopeViolations: 0,
		autonomy: 'auto',
		web: false,
		browser: 'none',
		shell: false,
		skills: [],
		status: 'active',
		ready: false
	};
}

describe('AgentTable', () => {
	it('virtualizes 10,000 rows keeping table DOM nodes < 200 and loads next page on bottom scroll', async () => {
		const agents = Array.from({ length: 10000 }, (_, i) => makeAgent(i + 1));
		const onLoadMore = vi.fn();

		const { container } = render(AgentTable, {
			props: {
				agents,
				onLoadMore
			}
		});

		const table = container.querySelector('table') as HTMLElement;
		expect(table).not.toBeNull();
		const domNodes = table.querySelectorAll('*').length;
		expect(domNodes).toBeLessThan(200);

		const scroller = container.querySelector('[data-table-scroller]') as HTMLElement;
		Object.defineProperty(scroller, 'clientHeight', { value: 320, configurable: true });
		Object.defineProperty(scroller, 'scrollHeight', { value: 400000, configurable: true });
		scroller.scrollTop = 399700;
		await fireEvent.scroll(scroller);

		expect(onLoadMore).toHaveBeenCalledTimes(1);
	});
});
