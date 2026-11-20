import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError } from './client.js';

describe('typed API client', () => {
	afterEach(() => {
		vi.restoreAllMocks();
	});

	it('converts non-2xx HTTP responses into ApiError with code and status', async () => {
		vi.stubGlobal(
			'fetch',
			vi.fn().mockResolvedValue(
				new Response(JSON.stringify({ error: 'invalid credentials', code: 'auth_failed' }), {
					status: 401,
					headers: { 'Content-Type': 'application/json' }
				})
			)
		);

		await expect(api.login({ password: 'wrong-password' })).rejects.toMatchObject({
			name: 'ApiError',
			status: 401,
			code: 'auth_failed',
			message: 'invalid credentials'
		});

		try {
			await api.login({ password: 'wrong-password' });
		} catch (err) {
			expect(err).toBeInstanceOf(ApiError);
		}
	});

	it('automatically attaches Idempotency-Key header on all mutating requests', async () => {
		const fetchMock = vi.fn().mockImplementation(async (_url: string, init?: RequestInit) => {
			const status = init?.method === 'POST' ? 202 : 200;
			return new Response(JSON.stringify({ messageId: 'm1', wakeupId: 'w1' }), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		});
		vi.stubGlobal('fetch', fetchMock);

		await api.sendMessage('thread-1', { content: 'Xin chào' });
		expect(fetchMock).toHaveBeenCalledTimes(1);
		const [, init1] = fetchMock.mock.calls[0] as [string, RequestInit];
		const headers1 = new Headers(init1.headers);
		expect(headers1.get('Idempotency-Key')).toBeTruthy();

		// GET requests should not send Idempotency-Key
		fetchMock.mockResolvedValueOnce(
			new Response(JSON.stringify({ status: 'ok' }), {
				status: 200,
				headers: { 'Content-Type': 'application/json' }
			})
		);
		await api.healthz();
		const [, initGet] = fetchMock.mock.calls[1] as [string, RequestInit];
		const headersGet = new Headers(initGet.headers);
		expect(headersGet.get('Idempotency-Key')).toBeNull();
	});
});
