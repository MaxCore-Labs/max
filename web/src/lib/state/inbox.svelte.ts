import { api, type Schemas } from '#lib/api/client.js';

export class InboxState {
	items = $state<Schemas['InboxItem'][]>([]);
	loading = $state(false);
	unreadCount = $derived(this.items.filter((it) => it.readAt === null || it.readAt === undefined).length);

	load = async (unreadOnly = false): Promise<void> => {
		this.loading = true;
		try {
			const res = await api.listInbox({ unreadOnly: unreadOnly || undefined, limit: 50 });
			this.items = res.items;
		} finally {
			this.loading = false;
		}
	};

	markRead = async (itemId: string): Promise<void> => {
		await api.markInboxRead(itemId);
		const now = Date.now();
		this.items = this.items.map((it) => (it.id === itemId ? { ...it, readAt: it.readAt ?? now } : it));
	};

	answer = async (itemId: string, answerText: string): Promise<Schemas['InboxItem']> => {
		const updated = await api.answerOwnerQuestion(itemId, { answer: answerText });
		this.items = this.items.map((it) => (it.id === itemId ? updated : it));
		return updated;
	};
}

export const inbox = new InboxState();
