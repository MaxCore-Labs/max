import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '#lib/api/client.js';
import { InboxState } from './inbox.svelte.js';

describe('InboxState unread badge and owner question reply', () => {
	afterEach(() => {
		vi.restoreAllMocks();
	});

	it('computes unreadCount from items without readAt and calls answerOwnerQuestion', async () => {
		const items = [
			{
				id: 'in-1',
				kind: 'owner_question',
				title: 'Chọn kênh xuất bản?',
				body: 'Agent hỏi nên đăng blog hay gửi bản tin trước.',
				choices: ['Blog', 'Bản tin'],
				urgency: 'normal',
				readAt: null,
				resolvedAt: null,
				createdAt: 1000
			},
			{
				id: 'in-2',
				kind: 'notice',
				title: 'Đã đồng bộ AGENT.md',
				body: 'Hoàn tất.',
				choices: [],
				urgency: 'low',
				readAt: 1500,
				resolvedAt: null,
				createdAt: 900
			},
			{
				id: 'in-3',
				kind: 'approval',
				title: 'Cần duyệt gửi email',
				body: 'Chi tiết...',
				choices: [],
				urgency: 'high',
				readAt: null,
				resolvedAt: null,
				createdAt: 800
			}
		];

		vi.spyOn(api, 'listInbox').mockResolvedValue({ items });
		const ansSpy = vi.spyOn(api, 'answerOwnerQuestion').mockResolvedValue({
			...items[0]!,
			readAt: 2000,
			resolvedAt: 2000
		});

		const store = new InboxState();
		await store.load();

		expect(store.unreadCount).toBe(2);

		await store.answer('in-1', 'Blog');
		expect(ansSpy).toHaveBeenCalledWith('in-1', { answer: 'Blog' });
		expect(store.unreadCount).toBe(1);
	});
});
