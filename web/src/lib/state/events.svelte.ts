import { SvelteSet } from 'svelte/reactivity';

export interface StreamEvent<T = unknown> {
	topic: string;
	type: string;
	data: T;
}

export type StreamHandler = (ev: StreamEvent) => void;
export type ResyncHandler = () => void | Promise<void>;

interface Subscription {
	topics: SvelteSet<string>;
	handler: StreamHandler;
	resync?: ResyncHandler;
}

const KNOWN_EVENT_TYPES = [
	'status',
	'inbox',
	'inbox.created',
	'inbox.updated',
	'approval.created',
	'approval.decided',
	'message.created',
	'run.started',
	'run.text',
	'run.ended',
	'task.updated',
	'agent.updated',
	'system.notice'
] as const;

export class EventStreamManager {
	connected = $state(false);
	reconnectCount = $state(0);

	private subs = new SvelteSet<Subscription>();
	private source: EventSource | null = null;
	private activeTopics = new SvelteSet<string>();
	private retryTimer: ReturnType<typeof setTimeout> | null = null;
	private retryAttempt = 0;
	private baseDelayMs: number;
	private maxDelayMs: number;

	constructor(opts?: { baseDelayMs?: number; maxDelayMs?: number }) {
		this.baseDelayMs = opts?.baseDelayMs ?? 500;
		this.maxDelayMs = opts?.maxDelayMs ?? 5000;
	}

	subscribe = (
		topics: string[],
		handler: StreamHandler,
		resync?: ResyncHandler
	): (() => void) => {
		const cleaned = topics.map((t) => t.trim()).filter(Boolean);
		const sub: Subscription = {
			topics: new SvelteSet(cleaned),
			handler,
			resync
		};
		this.subs.add(sub);
		this.ensureSource();

		return () => {
			this.subs.delete(sub);
			if (this.subs.size === 0) {
				this.closeSource();
			}
		};
	};

	private desiredTopics(): string[] {
		const all = new SvelteSet<string>(['status', 'inbox']);
		for (const sub of this.subs) {
			for (const t of sub.topics) {
				all.add(t);
			}
		}
		return Array.from(all).sort();
	}

	private ensureSource(): void {
		if (typeof EventSource === 'undefined' || this.subs.size === 0) {
			return;
		}
		const wanted = this.desiredTopics();
		const needsNewTopic = wanted.some((t) => !this.activeTopics.has(t));

		if (this.source && !needsNewTopic) {
			return;
		}
		if (this.retryTimer !== null && !needsNewTopic) {
			return;
		}
		this.closeSource();
		this.openSource(wanted);
	}

	private openSource(topics: string[]): void {
		if (this.subs.size === 0 || typeof EventSource === 'undefined') {
			return;
		}
		this.activeTopics = new SvelteSet(topics);
		const url = `/api/v1/events?topics=${encodeURIComponent(topics.join(','))}`;
		const es = new EventSource(url);
		this.source = es;

		es.onopen = () => {
			this.connected = true;
			this.retryAttempt = 0;
		};

		es.onerror = () => {
			if (this.source !== es) return;
			this.connected = false;
			es.close();
			this.source = null;
			this.scheduleReconnect();
		};

		const dispatchRaw = (fallbackType: string) => (msg: MessageEvent<string>) => {
			try {
				const parsed = JSON.parse(msg.data) as Partial<StreamEvent>;
				const ev: StreamEvent = {
					topic: parsed.topic ?? '',
					type: parsed.type ?? fallbackType,
					data: parsed.data ?? parsed
				};
				for (const sub of this.subs) {
					if (sub.topics.has(ev.topic)) {
						sub.handler(ev);
					}
				}
			} catch {
				// Ignore malformed frames
			}
		};

		es.onmessage = dispatchRaw('message');
		for (const evType of KNOWN_EVENT_TYPES) {
			es.addEventListener(evType, dispatchRaw(evType));
		}
	}

	private scheduleReconnect(): void {
		if (this.subs.size === 0 || this.retryTimer !== null) {
			return;
		}
		const delay = Math.min(this.maxDelayMs, this.baseDelayMs * Math.pow(2, this.retryAttempt));
		this.retryAttempt += 1;
		this.retryTimer = setTimeout(async () => {
			this.retryTimer = null;
			if (this.subs.size === 0) return;

			const resyncPromises: Promise<unknown>[] = [];
			for (const sub of this.subs) {
				if (sub.resync) {
					resyncPromises.push(Promise.resolve(sub.resync()));
				}
			}
			if (resyncPromises.length > 0) {
				await Promise.allSettled(resyncPromises);
			}
			this.reconnectCount += 1;
			this.openSource(this.desiredTopics());
		}, delay);
	}

	private closeSource(): void {
		if (this.retryTimer !== null) {
			clearTimeout(this.retryTimer);
			this.retryTimer = null;
		}
		if (this.source) {
			this.source.close();
			this.source = null;
		}
		this.activeTopics.clear();
		this.connected = false;
	}
}

export const events = new EventStreamManager();
