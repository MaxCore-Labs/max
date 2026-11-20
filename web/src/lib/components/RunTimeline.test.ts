import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { Schemas } from '#lib/api/client.js';
import RunTimeline from './RunTimeline.svelte';

describe('RunTimeline', () => {
	it('displays called tools, policy decisions, and reasons', () => {
		const events: Schemas['RunEvent'][] = [
			{
				id: 1,
				runId: 'run-1',
				agentId: 'ag-1',
				type: 'tool_call',
				data: JSON.stringify({
					toolName: 'wiki.write',
					verdict: 'allow',
					reason: 'nằm trong phạm vi wiki/'
				}),
				createdAt: 1000
			},
			{
				id: 2,
				runId: 'run-1',
				agentId: 'ag-1',
				type: 'policy',
				data: JSON.stringify({
					toolName: 'gmail.send',
					verdict: 'ask',
					reason: 'lớp E chưa có grant'
				}),
				createdAt: 2000
			}
		];

		render(RunTimeline, { props: { events } });

		expect(screen.getByText('wiki.write')).toBeInTheDocument();
		expect(screen.getByText(/allow/i)).toBeInTheDocument();
		expect(screen.getByText(/nằm trong phạm vi wiki\//)).toBeInTheDocument();

		expect(screen.getByText('gmail.send')).toBeInTheDocument();
		expect(screen.getByText(/ask/i)).toBeInTheDocument();
		expect(screen.getByText(/lớp E chưa có grant/)).toBeInTheDocument();
	});
});
