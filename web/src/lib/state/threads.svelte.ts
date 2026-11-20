import { api, type Schemas } from '#lib/api/client.js';
import { events, type EventStreamManager, type StreamEvent } from '#lib/state/events.svelte.js';

function mergeMessages(
	existing: Schemas['Message'][],
	incoming: Schemas['Message'][]
): Schemas['Message'][] {
	const byId = new Map<string, Schemas['Message']>();
	for (const m of existing) byId.set(m.id, m);
	for (const m of incoming) byId.set(m.id, m);
	return Array.from(byId.values()).sort((a, b) =>
		a.createdAt !== b.createdAt ? a.createdAt - b.createdAt : a.id.localeCompare(b.id)
	);
}

export class ThreadsState {
	list = $state<Schemas['Thread'][]>([]);
	activeId = $state<string | null>(null);
	messages = $state<Schemas['Message'][]>([]);
	liveText = $state('');
	activeRunId = $state<string | null>(null);
	busyWith = $state<Schemas['BusyInfo'] | null>(null);
	approvals = $state<Schemas['Approval'][]>([]);
	loading = $state(false);

	private stream: EventStreamManager;
	private unsub: (() => void) | null = null;

	constructor(stream: EventStreamManager = events) {
		this.stream = stream;
	}

	loadList = async (): Promise<void> => {
		const res = await api.listThreads({ limit: 50 });
		this.list = res.items;
		if (!this.activeId && res.items[0]) {
			await this.selectThread(res.items[0].id);
		}
	};

	selectThread = async (threadId: string): Promise<void> => {
		this.activeId = threadId;
		this.unsub?.();
		this.unsub = null;

		await this.reloadActive(true);

		this.unsub = this.stream.subscribe(
			[`thread:${threadId}`, 'inbox'],
			this.handleEvent,
			() => this.reloadActive(false)
		);
	};

	reloadActive = async (resetMessages = false): Promise<void> => {
		const id = this.activeId;
		if (!id) return;
		this.loading = true;
		try {
			const [snap, appRes] = await Promise.all([
				api.getThread(id),
				api.listApprovals({ status: 'pending', limit: 50 })
			]);
			if (this.activeId !== id) return;
			this.messages = mergeMessages(resetMessages ? [] : this.messages, snap.messages);
			this.activeRunId = snap.activeRunId ?? null;
			this.liveText = snap.liveText ?? '';
			this.busyWith = snap.busyWith ?? null;
			this.approvals = appRes.items.filter((a) => a.agentId === snap.thread.agentId);
		} finally {
			this.loading = false;
		}
	};

	createThread = async (title?: string): Promise<Schemas['Thread']> => {
		const created = await api.createThread({ title });
		this.list = [created, ...this.list.filter((t) => t.id !== created.id)];
		await this.selectThread(created.id);
		return created;
	};

	send = async (content: string): Promise<void> => {
		let targetId = this.activeId;
		if (!targetId) {
			const created = await this.createThread();
			targetId = created.id;
		}
		await api.sendMessage(targetId, { content });
		await this.reloadActive(false);
	};

	resetActive = async (): Promise<void> => {
		if (!this.activeId) return;
		await api.resetThread(this.activeId);
		this.liveText = '';
	};

	dispose = (): void => {
		this.unsub?.();
		this.unsub = null;
	};

	private handleEvent = (ev: StreamEvent): void => {
		const id = this.activeId;
		if (!id) return;
		const expectedTopic = `thread:${id}`;

		if (ev.topic === expectedTopic) {
			if (ev.type === 'run.text') {
				const d = ev.data as { delta?: string; text?: string };
				this.liveText += d.delta ?? d.text ?? '';
				return;
			}
			if (ev.type === 'message.created') {
				const msg = ev.data as Schemas['Message'];
				if (msg.role === 'assistant') {
					this.liveText = '';
				}
				this.messages = mergeMessages(this.messages, [msg]);
				return;
			}
			if (ev.type === 'run.ended') {
				this.liveText = '';
				void this.reloadActive(false);
				return;
			}
		}

		if (ev.type === 'approval.created' || ev.type === 'approval.decided') {
			void this.reloadActive(false);
		}
	};
}

export const threads = new ThreadsState();
