import { render, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import type { Schemas } from '#lib/api/client.js';
import type { EventStreamManager, StreamHandler } from '#lib/state/events.svelte.js';
import TaskThread from './TaskThread.svelte';

describe('TaskThread', () => {
	it('renders comments via MarkdownView and appends SSE task:<id> events without duplicates', async () => {
		let capturedHandler: StreamHandler | null = null;
		const fakeStream = {
			subscribe: vi.fn((_topics: string[], handler: StreamHandler) => {
				capturedHandler = handler;
				return () => {};
			})
		} as unknown as EventStreamManager;

		const initial: Schemas['TaskEvent'][] = [
			{
				id: 'ev-1',
				taskId: 'tk-1',
				kind: 'comment',
				actorKind: 'agent',
				actorId: 'writer-1',
				body: '**Bản nháp đầu tiên** đã xong',
				createdAt: 1000
			}
		];

		const { container } = render(TaskThread, {
			props: {
				taskId: 'tk-1',
				initialEvents: initial,
				stream: fakeStream
			}
		});

		await waitFor(() => {
			const strong = container.querySelector('.markdown-body strong');
			expect(strong?.textContent).toBe('Bản nháp đầu tiên');
		});

		expect(capturedHandler).not.toBeNull();

		const newEv: Schemas['TaskEvent'] = {
			id: 'ev-2',
			taskId: 'tk-1',
			kind: 'comment',
			actorKind: 'owner',
			body: 'Đã duyệt nội dung',
			createdAt: 2000
		};

		// Emit duplicate ev-1 and new ev-2 twice
		capturedHandler!({ topic: 'task:tk-1', type: 'task.comment', data: initial[0] });
		capturedHandler!({ topic: 'task:tk-1', type: 'task.comment', data: newEv });
		capturedHandler!({ topic: 'task:tk-1', type: 'task.comment', data: newEv });

		await waitFor(() => {
			const items = container.querySelectorAll('[data-task-event]');
			expect(items.length).toBe(2);
		});
	});
});
