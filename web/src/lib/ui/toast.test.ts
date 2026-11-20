import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ToastStore } from './toast.svelte.js';

describe('toast store timers', () => {
	beforeEach(() => {
		vi.useFakeTimers();
	});

	afterEach(() => {
		vi.useRealTimers();
	});

	it('auto-dismisses info toast after 5000ms and keeps error toast past 60000ms', () => {
		const store = new ToastStore();
		store.show({ kind: 'info', messageKey: 'Đã lưu cài đặt' });
		store.show({ kind: 'error', messageKey: 'Không lưu được cài đặt' });

		expect(store.items).toHaveLength(2);

		vi.advanceTimersByTime(5000);
		expect(store.items).toHaveLength(1);
		expect(store.items[0]?.kind).toBe('error');

		vi.advanceTimersByTime(60000);
		expect(store.items).toHaveLength(1);
		expect(store.items[0]?.kind).toBe('error');
	});
});
