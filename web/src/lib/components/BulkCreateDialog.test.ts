import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import type { Schemas } from '#lib/api/client.js';
import BulkCreateDialog from './BulkCreateDialog.svelte';

describe('BulkCreateDialog', () => {
	const templates: Schemas['RoleTemplate'][] = [
		{
			slug: 'monitor',
			name: 'Theo dõi',
			engines: ['agy'],
			autonomy: 'auto',
			web: true,
			browser: 'anon',
			sensitive: false,
			requiredParams: ['target', 'url']
		}
	];
	const teams: Schemas['Team'][] = [
		{
			id: 't1',
			slug: 'ops',
			name: 'Vận hành',
			mandate: { outcome: '99.9%', measures: [] },
			maxRunning: 2,
			running: 0,
			status: 'active',
			agentCount: 0
		}
	];

	it('previews 3 slugs when pasting 3 lines and highlights rows missing a required parameter', async () => {
		render(BulkCreateDialog, {
			props: {
				open: true,
				templates,
				teams,
				oncreated: vi.fn()
			}
		});

		const textarea = document.getElementById('bulk-paste-input') as HTMLTextAreaElement;
		await fireEvent.input(textarea, {
			target: {
				value: 'Competitor A, https://a.example.com\nCompetitor B, https://b.example.com\nOnlyTargetNoUrl'
			}
		});

		expect(screen.getByText('monitor-1')).toBeInTheDocument();
		expect(screen.getByText('monitor-2')).toBeInTheDocument();
		expect(screen.getByText('monitor-3')).toBeInTheDocument();

		const invalidRow = screen.getByTestId('bulk-preview-row-2');
		expect(invalidRow.textContent).toContain('url');
	});
});
