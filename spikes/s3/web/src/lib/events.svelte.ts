export interface ServerTickEvent {
	seq: number;
	serverSentMs: number;
	receivedAtMs: number;
	latencyMs: number;
	topic: string;
}

class LiveEventsStream {
	connected = $state(false);
	lastEvent = $state<ServerTickEvent | null>(null);
	history = $state<ServerTickEvent[]>([]);
	private source: EventSource | null = null;

	connect(): () => void {
		if (typeof window === 'undefined' || this.source) {
			return () => {};
		}
		const es = new EventSource('/api/v1/events?topics=status');
		this.source = es;

		es.onopen = () => {
			this.connected = true;
		};

		es.onerror = () => {
			this.connected = false;
		};

		es.addEventListener('tick', (ev: MessageEvent<string>) => {
			const receivedAtMs = Date.now();
			try {
				const raw = JSON.parse(ev.data) as {
					seq: number;
					serverSentMs: number;
					topic: string;
				};
				const item: ServerTickEvent = {
					seq: raw.seq,
					serverSentMs: raw.serverSentMs,
					receivedAtMs,
					latencyMs: Math.max(0, receivedAtMs - raw.serverSentMs),
					topic: raw.topic
				};
				this.connected = true;
				this.lastEvent = item;
				this.history = [item, ...this.history.slice(0, 19)];
			} catch {
				// Ignore malformed frames
			}
		});

		return () => {
			es.close();
			this.source = null;
			this.connected = false;
		};
	}
}

export const liveEvents = new LiveEventsStream();
