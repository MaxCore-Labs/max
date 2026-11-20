import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '#lib/api/client.js';
import { EventStreamManager } from '#lib/state/events.svelte.js';
import { ThreadsState } from './threads.svelte.js';

describe('ThreadsState streaming and resync deduplication', () => {
	afterEach(() => {
		vi.restoreAllMocks();
	});

	it('appends run.text to active thread bubble, replaces it on message.created, and deduplicates on resync', async () => {
		const snapInitial = {
			thread: {
				id: 't1',
				agentId: 'ag1',
				title: 'Chat chính',
				createdAt: 1000,
				updatedAt: 1000
			},
			messages: [
				{
					id: 'm1',
					threadId: 't1',
					role: 'user' as const,
					content: 'Xin chào',
					attachments: [],
					createdAt: 1000
				}
			],
			activeRunId: 'run-1',
			liveText: 'Đang '
		};

		const getThreadSpy = vi.spyOn(api, 'getThread').mockResolvedValue(snapInitial);
		vi.spyOn(api, 'listApprovals').mockResolvedValue({ items: [] });

		const stream = new EventStreamManager({ baseDelayMs: 20 });
		let capturedHandler: ((ev: { topic: string; type: string; data: unknown }) => void) | undefined;
		let capturedResync: (() => void | Promise<void>) | undefined;
		vi.spyOn(stream, 'subscribe').mockImplementation((_topics, handler, resync) => {
			capturedHandler = handler;
			capturedResync = resync;
			return () => {};
		});

		const store = new ThreadsState(stream);
		await store.selectThread('t1');

		expect(store.liveText).toBe('Đang ');
		expect(store.messages).toHaveLength(1);

		// 1. run.text for thread:t1 appends to liveText; other threads ignored
		capturedHandler?.({ topic: 'thread:t2', type: 'run.text', data: { delta: 'NHẦM' } });
		capturedHandler?.({ topic: 'thread:t1', type: 'run.text', data: { delta: 'trả lời...' } });
		expect(store.liveText).toBe('Đang trả lời...');

		// 2. message.created replaces temporary streaming bubble with persisted assistant message
		const m2 = {
			id: 'm2',
			threadId: 't1',
			role: 'assistant' as const,
			content: 'Đang trả lời... xong!',
			attachments: [],
			runId: 'run-1',
			createdAt: 2000
		};
		capturedHandler?.({ topic: 'thread:t1', type: 'message.created', data: m2 });
		expect(store.liveText).toBe('');
		expect(store.messages.map((m) => m.id)).toEqual(['m1', 'm2']);

		// 3. Disconnect + resync returns snapshot containing [m2, m1] (newest-first from API) -> no duplicates!
		getThreadSpy.mockResolvedValueOnce({
			thread: snapInitial.thread,
			messages: [m2, snapInitial.messages[0]!],
			activeRunId: null
		});
		await capturedResync?.();

		expect(store.messages).toHaveLength(2);
		expect(store.messages.map((m) => m.id)).toEqual(['m1', 'm2']);
	});
});
