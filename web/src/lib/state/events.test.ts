import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { EventStreamManager } from './events.svelte.js';

class MockEventSource {
	static instances: MockEventSource[] = [];
	static CONNECTING = 0;
	static OPEN = 1;
	static CLOSED = 2;

	url: string;
	readyState = MockEventSource.CONNECTING;
	onopen: (() => void) | null = null;
	onerror: (() => void) | null = null;
	onmessage: ((ev: MessageEvent<string>) => void) | null = null;
	private listeners = new Map<string, Set<(ev: MessageEvent<string>) => void>>();

	constructor(url: string) {
		this.url = url;
		MockEventSource.instances.push(this);
	}

	addEventListener(type: string, cb: (ev: MessageEvent<string>) => void) {
		let set = this.listeners.get(type);
		if (!set) {
			set = new Set();
			this.listeners.set(type, set);
		}
		set.add(cb);
	}

	emitOpen() {
		this.readyState = MockEventSource.OPEN;
		this.onopen?.();
	}

	emitError() {
		this.readyState = MockEventSource.CLOSED;
		this.onerror?.();
	}

	emitEvent(type: string, payload: unknown) {
		const ev = new MessageEvent(type, { data: JSON.stringify(payload) });
		if (type === 'message') {
			this.onmessage?.(ev);
		}
		for (const cb of this.listeners.get(type) ?? []) {
			cb(ev);
		}
	}

	close() {
		this.readyState = MockEventSource.CLOSED;
	}
}

describe('events single EventSource manager', () => {
	beforeEach(() => {
		vi.useFakeTimers();
		MockEventSource.instances = [];
		vi.stubGlobal('EventSource', MockEventSource);
	});

	afterEach(() => {
		vi.useRealTimers();
		vi.unstubAllGlobals();
	});

	it('creates only 1 EventSource across two subscribe calls', () => {
		const mgr = new EventStreamManager({ baseDelayMs: 50 });
		const h1 = vi.fn();
		const h2 = vi.fn();

		const unsub1 = mgr.subscribe(['status'], h1);
		const unsub2 = mgr.subscribe(['inbox'], h2);

		expect(MockEventSource.instances).toHaveLength(1);

		const es = MockEventSource.instances[0]!;
		es.emitOpen();
		es.emitEvent('status', { topic: 'status', type: 'status', data: { ok: true } });

		expect(h1).toHaveBeenCalledTimes(1);
		expect(h2).toHaveBeenCalledTimes(0);

		unsub1();
		unsub2();
	});

	it('calls resync and reconnects after connection loss', async () => {
		const mgr = new EventStreamManager({ baseDelayMs: 50 });
		const order: string[] = [];
		const resync = vi.fn(async () => {
			order.push('resync');
		});
		const handler = vi.fn();

		const unsub = mgr.subscribe(['status'], handler, resync);
		expect(MockEventSource.instances).toHaveLength(1);
		const first = MockEventSource.instances[0]!;
		first.emitOpen();

		// Simulate connection drop
		first.emitError();
		expect(mgr.connected).toBe(false);

		await vi.advanceTimersByTimeAsync(60);

		expect(resync).toHaveBeenCalledTimes(1);
		expect(MockEventSource.instances).toHaveLength(2);
		order.push('reconnected');
		expect(order).toEqual(['resync', 'reconnected']);

		MockEventSource.instances[1]!.emitOpen();
		expect(mgr.connected).toBe(true);
		unsub();
	});
});
