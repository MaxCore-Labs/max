export interface ServerTickEvent {
	id: number;
	bootId: string;
	seq: number;
	serverSentMs: number;
	receivedAtMs: number;
	latencyMs: number;
	topic: string;
}

class LiveEventsStream {
	connected = $state(false);
	bootId = $state<string | null>(null);
	reconnectCount = $state(0);
	lastEvent = $state<ServerTickEvent | null>(null);
	history = $state<ServerTickEvent[]>([]);

	private source: EventSource | null = null;
	private retryTimer: ReturnType<typeof setTimeout> | null = null;
	private retryAttempt = 0;
	private hadError = false;
	private disposed = false;
	private nextId = 1;

	connect(): () => void {
		if (typeof window === 'undefined' || this.source || this.retryTimer) {
			return () => {};
		}
		this.disposed = false;
		this.openSource();

		return () => {
			this.disposed = true;
			if (this.retryTimer !== null) {
				clearTimeout(this.retryTimer);
				this.retryTimer = null;
			}
			this.source?.close();
			this.source = null;
			this.connected = false;
		};
	}

	private openSource(): void {
		if (this.disposed || this.source) {
			return;
		}
		const es = new EventSource('/api/v1/events?topics=status');
		this.source = es;

		es.onopen = () => {
			this.connected = true;
			this.retryAttempt = 0;
			if (this.hadError) {
				this.hadError = false;
				this.reconnectCount += 1;
				this.lastEvent = null;
				this.history = [];
			}
		};

		es.onerror = () => {
			this.connected = false;
			this.hadError = true;
			// Native EventSource stops retrying permanently if the endpoint returns
			// non-200 (e.g. reverse-proxy 502/503 during daemon restart) or wrong MIME.
			if (es.readyState === EventSource.CLOSED && !this.disposed) {
				es.close();
				if (this.source === es) {
					this.source = null;
				}
				this.scheduleReconnect();
			}
		};

		es.addEventListener('tick', (ev: MessageEvent<string>) => {
			const receivedAtMs = Date.now();
			try {
				const raw = JSON.parse(ev.data) as {
					bootId?: string;
					seq: number;
					serverSentMs: number;
					topic: string;
				};
				const incomingBoot = raw.bootId ?? 'default';
				const resetHistory = this.bootId !== null && this.bootId !== incomingBoot;
				this.bootId = incomingBoot;

				const item: ServerTickEvent = {
					id: this.nextId++,
					bootId: incomingBoot,
					seq: raw.seq,
					serverSentMs: raw.serverSentMs,
					receivedAtMs,
					latencyMs: Math.max(0, receivedAtMs - raw.serverSentMs),
					topic: raw.topic
				};
				this.connected = true;
				this.lastEvent = item;
				this.history = resetHistory ? [item] : [item, ...this.history.slice(0, 19)];
			} catch {
				// Ignore malformed SSE frames
			}
		});
	}

	private scheduleReconnect(): void {
		if (this.disposed || this.retryTimer !== null) {
			return;
		}
		const delayMs = Math.min(5000, 500 * Math.pow(2, this.retryAttempt));
		this.retryAttempt += 1;
		this.retryTimer = setTimeout(() => {
			this.retryTimer = null;
			this.openSource();
		}, delayMs);
	}
}

export const liveEvents = new LiveEventsStream();
