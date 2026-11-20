import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import CapacityPanel from './CapacityPanel.svelte';

describe('CapacityPanel', () => {
	it('renders engine slots, priority backlog ages/reasons, and quota-delayed count', () => {
		render(CapacityPanel, {
			props: {
				engines: [
					{
						name: 'agy',
						enabled: true,
						installed: true,
						loggedIn: true,
						slots: 3,
						used: 3
					}
				],
				queue: {
					total: 6,
					byPriority: { '30': 2, '20': 3, '10': 1 },
					quotaDelayed: 3,
					backlog: [
						{ priority: 30, count: 2, oldestAgeSec: 45, reason: 'slots_full' },
						{ priority: 20, count: 3, oldestAgeSec: 120, reason: 'quota_limited' },
						{ priority: 10, count: 1, oldestAgeSec: 15, reason: 'cooldown' }
					]
				}
			}
		});

		expect(screen.getByText(/3\/3/)).toBeInTheDocument();
		expect(screen.getByText(/Hết chỗ/i)).toBeInTheDocument();
		expect(screen.getByText(/Hết hạn mức/i)).toBeInTheDocument();
		expect(screen.getByText(/Cooldown/i)).toBeInTheDocument();
		expect(screen.getByText(/45\s*giây/i)).toBeInTheDocument();
		expect(screen.getByText(/120\s*giây/i)).toBeInTheDocument();
		expect(screen.getByText(/Việc trễ vì hạn mức:\s*3/i)).toBeInTheDocument();
	});
});
