import { afterEach, describe, expect, it, vi } from 'vitest';
import { AgentsState } from './agents.svelte.js';
import type { EventStreamManager, StreamHandler } from './events.svelte.js';

describe('AgentsState SSE team counts', () => {
	afterEach(() => {
		vi.restoreAllMocks();
	});

	it('updates team count on team:<id> SSE event without reloading the full agent list', async () => {
		let capturedHandler: StreamHandler | null = null;
		const fakeEvents = {
			subscribe: vi.fn((_topics: string[], handler: StreamHandler) => {
				capturedHandler = handler;
				return () => {};
			})
		} as unknown as EventStreamManager;

		const fetchMock = vi.fn().mockImplementation(async (url: string) => {
			if (url.startsWith('/api/v1/teams')) {
				return new Response(
					JSON.stringify({
						items: [
							{
								id: 'team-1',
								slug: 'core',
								name: 'Core',
								mandate: { outcome: 'Ship', measures: [] },
								maxRunning: 3,
								running: 1,
								status: 'active',
								agentCount: 2
							}
						]
					}),
					{ status: 200, headers: { 'Content-Type': 'application/json' } }
				);
			}
			if (url.startsWith('/api/v1/templates')) {
				return new Response(JSON.stringify({ items: [] }), {
					status: 200,
					headers: { 'Content-Type': 'application/json' }
				});
			}
			return new Response(JSON.stringify({ items: [] }), {
				status: 200,
				headers: { 'Content-Type': 'application/json' }
			});
		});
		vi.stubGlobal('fetch', fetchMock);

		const state = new AgentsState(fakeEvents);
		await state.loadInitial();

		const callsAfterInit = fetchMock.mock.calls.length;
		expect(state.teams[0]?.agentCount).toBe(2);
		expect(capturedHandler).not.toBeNull();

		capturedHandler!({
			topic: 'team:team-1',
			type: 'team.counts',
			data: { teamId: 'team-1', agentCount: 7, running: 2 }
		});

		expect(state.teams[0]?.agentCount).toBe(7);
		expect(state.teams[0]?.running).toBe(2);
		expect(fetchMock.mock.calls.length).toBe(callsAfterInit);
	});
});
