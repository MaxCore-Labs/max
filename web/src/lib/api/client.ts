import type { components, operations } from './schema.js';

export type Schemas = components['schemas'];

export class ApiError extends Error {
	readonly status: number;
	readonly code: string;

	constructor(status: number, code: string, message: string) {
		super(message);
		this.name = 'ApiError';
		this.status = status;
		this.code = code;
	}
}

export interface RequestOptions {
	idempotencyKey?: string;
	signal?: AbortSignal;
}

function buildQuery(params?: Record<string, string | number | boolean | undefined>): string {
	if (!params) return '';
	const sp = new URLSearchParams();
	for (const [k, v] of Object.entries(params)) {
		if (v !== undefined) {
			sp.set(k, String(v));
		}
	}
	const qs = sp.toString();
	return qs ? `?${qs}` : '';
}

function defaultErrorCode(status: number): string {
	switch (status) {
		case 400:
			return 'bad_request';
		case 401:
			return 'unauthorized';
		case 403:
			return 'forbidden';
		case 404:
			return 'not_found';
		case 409:
			return 'conflict';
		case 429:
			return 'rate_limited';
		case 503:
			return 'service_unavailable';
		default:
			return `http_${status}`;
	}
}

export async function apiRequest<T>(
	method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE',
	path: string,
	body?: unknown,
	opts?: RequestOptions
): Promise<T> {
	const headers = new Headers();
	if (method !== 'GET') {
		headers.set('Idempotency-Key', opts?.idempotencyKey ?? crypto.randomUUID());
	}
	if (body !== undefined) {
		headers.set('Content-Type', 'application/json');
	}

	const res = await fetch(path, {
		method,
		headers,
		credentials: 'same-origin',
		body: body !== undefined ? JSON.stringify(body) : undefined,
		signal: opts?.signal
	});

	if (!res.ok) {
		let message = res.statusText || `HTTP ${res.status}`;
		let code = defaultErrorCode(res.status);
		try {
			const errJson = (await res.json()) as { error?: string; code?: string };
			if (errJson.error) message = errJson.error;
			if (errJson.code) code = errJson.code;
		} catch {
			// Fall back to HTTP status text
		}
		throw new ApiError(res.status, code, message);
	}

	if (res.status === 204) {
		return undefined as T;
	}
	return (await res.json()) as T;
}

export const api = {
	healthz: () => apiRequest<Schemas['HealthResponse']>('GET', '/api/v1/healthz'),

	login: (req: Schemas['LoginRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['UserResponse']>('POST', '/api/v1/auth/login', req, opts),

	logout: (opts?: RequestOptions) => apiRequest<void>('POST', '/api/v1/auth/logout', undefined, opts),

	getMe: () => apiRequest<Schemas['UserResponse']>('GET', '/api/v1/auth/me'),

	changePassword: (req: Schemas['ChangePasswordRequest'], opts?: RequestOptions) =>
		apiRequest<void>('POST', '/api/v1/auth/password', req, opts),

	listThreads: (q?: operations['listThreads']['parameters']['query']) =>
		apiRequest<Schemas['ThreadListResponse']>('GET', `/api/v1/threads${buildQuery(q)}`),

	createThread: (req: Schemas['CreateThreadRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['Thread']>('POST', '/api/v1/threads', req, opts),

	getThread: (threadId: string) =>
		apiRequest<Schemas['ThreadSnapshot']>('GET', `/api/v1/threads/${encodeURIComponent(threadId)}`),

	listMessages: (threadId: string, q?: operations['listMessages']['parameters']['query']) =>
		apiRequest<Schemas['MessageListResponse']>(
			'GET',
			`/api/v1/threads/${encodeURIComponent(threadId)}/messages${buildQuery(q)}`
		),

	sendMessage: (threadId: string, req: Schemas['SendMessageRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['SendMessageResponse']>(
			'POST',
			`/api/v1/threads/${encodeURIComponent(threadId)}/messages`,
			req,
			opts
		),

	resetThread: (threadId: string, opts?: RequestOptions) =>
		apiRequest<void>('POST', `/api/v1/threads/${encodeURIComponent(threadId)}/reset`, undefined, opts),

	listRuns: (q?: operations['listRuns']['parameters']['query']) =>
		apiRequest<Schemas['RunListResponse']>('GET', `/api/v1/runs${buildQuery(q)}`),

	getRun: (runId: string) =>
		apiRequest<Schemas['Run']>('GET', `/api/v1/runs/${encodeURIComponent(runId)}`),

	listRunEvents: (runId: string, q?: operations['listRunEvents']['parameters']['query']) =>
		apiRequest<Schemas['RunEventListResponse']>(
			'GET',
			`/api/v1/runs/${encodeURIComponent(runId)}/events${buildQuery(q)}`
		),

	cancelRun: (runId: string, req?: Schemas['CancelRunRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['Run']>(
			'POST',
			`/api/v1/runs/${encodeURIComponent(runId)}/cancel`,
			req ?? { mode: 'requeue' },
			opts
		),

	listApprovals: (q?: operations['listApprovals']['parameters']['query']) =>
		apiRequest<Schemas['ApprovalListResponse']>('GET', `/api/v1/approvals${buildQuery(q)}`),

	decideApproval: (
		approvalId: string,
		req: Schemas['DecideApprovalRequest'],
		opts?: RequestOptions
	) =>
		apiRequest<Schemas['Approval']>(
			'POST',
			`/api/v1/approvals/${encodeURIComponent(approvalId)}/decide`,
			req,
			opts
		),

	listInbox: (q?: operations['listInbox']['parameters']['query']) =>
		apiRequest<Schemas['InboxListResponse']>('GET', `/api/v1/inbox${buildQuery(q)}`),

	markInboxRead: (itemId: string, opts?: RequestOptions) =>
		apiRequest<void>('POST', `/api/v1/inbox/${encodeURIComponent(itemId)}/read`, undefined, opts),

	answerOwnerQuestion: (
		itemId: string,
		req: Schemas['AnswerQuestionRequest'],
		opts?: RequestOptions
	) =>
		apiRequest<Schemas['InboxItem']>(
			'POST',
			`/api/v1/inbox/${encodeURIComponent(itemId)}/answer`,
			req,
			opts
		),

	getUsage: (q?: operations['getUsage']['parameters']['query']) =>
		apiRequest<Schemas['UsageResponse']>('GET', `/api/v1/usage${buildQuery(q)}`),

	getQuota: () => apiRequest<Schemas['QuotaResponse']>('GET', '/api/v1/quota'),

	updateQuotaSettings: (req: Schemas['UpdateQuotaSettingsRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['QuotaResponse']>('POST', '/api/v1/quota', req, opts),

	getStatus: () => apiRequest<Schemas['StatusResponse']>('GET', '/api/v1/status'),

	getSettings: () => apiRequest<Schemas['SettingsResponse']>('GET', '/api/v1/settings'),

	updateSettings: (req: Schemas['UpdateSettingsRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['SettingsResponse']>('POST', '/api/v1/settings', req, opts)
};
