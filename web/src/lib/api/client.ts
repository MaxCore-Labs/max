import type { components, operations } from './schema.js';

export type Schemas = components['schemas'];

export class ApiError extends Error {
	readonly status: number;
	readonly code: string;
	readonly field?: string;
	readonly activeRunId?: string;
	readonly activeTaskId?: string;

	constructor(
		status: number,
		code: string,
		message: string,
		extra?: { field?: string; activeRunId?: string; activeTaskId?: string }
	) {
		super(message);
		this.name = 'ApiError';
		this.status = status;
		this.code = code;
		this.field = extra?.field;
		this.activeRunId = extra?.activeRunId;
		this.activeTaskId = extra?.activeTaskId;
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
		if (v !== undefined) sp.set(k, String(v));
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
		let extra: { field?: string; activeRunId?: string; activeTaskId?: string } | undefined;
		try {
			const errJson = (await res.json()) as Schemas['ErrorResponse'];
			if (errJson.error) message = errJson.error;
			if (errJson.code) code = errJson.code;
			extra = {
				field: errJson.field,
				activeRunId: errJson.activeRunId,
				activeTaskId: errJson.activeTaskId
			};
		} catch {
			// Fall back to HTTP status text
		}
		throw new ApiError(res.status, code, message, extra);
	}

	if (res.status === 204) return undefined as T;
	return (await res.json()) as T;
}

const enc = encodeURIComponent;

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
		apiRequest<Schemas['ThreadSnapshot']>('GET', `/api/v1/threads/${enc(threadId)}`),
	listMessages: (threadId: string, q?: operations['listMessages']['parameters']['query']) =>
		apiRequest<Schemas['MessageListResponse']>('GET', `/api/v1/threads/${enc(threadId)}/messages${buildQuery(q)}`),
	sendMessage: (threadId: string, req: Schemas['SendMessageRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['SendMessageResponse']>('POST', `/api/v1/threads/${enc(threadId)}/messages`, req, opts),
	resetThread: (threadId: string, opts?: RequestOptions) =>
		apiRequest<void>('POST', `/api/v1/threads/${enc(threadId)}/reset`, undefined, opts),

	listRuns: (q?: operations['listRuns']['parameters']['query']) =>
		apiRequest<Schemas['RunListResponse']>('GET', `/api/v1/runs${buildQuery(q)}`),
	getRun: (runId: string) => apiRequest<Schemas['Run']>('GET', `/api/v1/runs/${enc(runId)}`),
	listRunEvents: (runId: string, q?: operations['listRunEvents']['parameters']['query']) =>
		apiRequest<Schemas['RunEventListResponse']>('GET', `/api/v1/runs/${enc(runId)}/events${buildQuery(q)}`),
	cancelRun: (runId: string, req?: Schemas['CancelRunRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['Run']>('POST', `/api/v1/runs/${enc(runId)}/cancel`, req ?? { mode: 'requeue' }, opts),

	listApprovals: (q?: operations['listApprovals']['parameters']['query']) =>
		apiRequest<Schemas['ApprovalListResponse']>('GET', `/api/v1/approvals${buildQuery(q)}`),
	decideApproval: (approvalId: string, req: Schemas['DecideApprovalRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['Approval']>('POST', `/api/v1/approvals/${enc(approvalId)}/decide`, req, opts),

	listInbox: (q?: operations['listInbox']['parameters']['query']) =>
		apiRequest<Schemas['InboxListResponse']>('GET', `/api/v1/inbox${buildQuery(q)}`),
	markInboxRead: (itemId: string, opts?: RequestOptions) =>
		apiRequest<void>('POST', `/api/v1/inbox/${enc(itemId)}/read`, undefined, opts),
	answerOwnerQuestion: (itemId: string, req: Schemas['AnswerQuestionRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['InboxItem']>('POST', `/api/v1/inbox/${enc(itemId)}/answer`, req, opts),

	getUsage: (q?: operations['getUsage']['parameters']['query']) =>
		apiRequest<Schemas['UsageResponse']>('GET', `/api/v1/usage${buildQuery(q)}`),
	getQuota: () => apiRequest<Schemas['QuotaResponse']>('GET', '/api/v1/quota'),
	updateQuotaSettings: (req: Schemas['UpdateQuotaSettingsRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['QuotaResponse']>('PUT', '/api/v1/quota', req, opts),
	getStatus: () => apiRequest<Schemas['StatusResponse']>('GET', '/api/v1/status'),
	getSettings: () => apiRequest<Schemas['SettingsResponse']>('GET', '/api/v1/settings'),
	updateSettings: (req: Schemas['UpdateSettingsRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['SettingsResponse']>('PUT', '/api/v1/settings', req, opts),

	listAgents: (q?: operations['listAgents']['parameters']['query']) =>
		apiRequest<Schemas['AgentListResponse']>('GET', `/api/v1/agents${buildQuery(q)}`),
	getAgent: (agentId: string) => apiRequest<Schemas['AgentDetail']>('GET', `/api/v1/agents/${enc(agentId)}`),
	createAgentFromTemplate: (req: Schemas['CreateAgentRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['AgentDetail']>('POST', '/api/v1/agents', req, opts),
	createAgentsBulk: (req: Schemas['CreateAgentsBulkRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['CreateAgentsBulkResponse']>('POST', '/api/v1/agents/bulk', req, opts),
	updateAgent: (agentId: string, req: Schemas['UpdateAgentRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['AgentDetail']>('PATCH', `/api/v1/agents/${enc(agentId)}`, req, opts),
	pauseAgent: (agentId: string, opts?: RequestOptions) =>
		apiRequest<Schemas['Agent']>('POST', `/api/v1/agents/${enc(agentId)}/pause`, undefined, opts),
	resumeAgent: (agentId: string, opts?: RequestOptions) =>
		apiRequest<Schemas['Agent']>('POST', `/api/v1/agents/${enc(agentId)}/resume`, undefined, opts),
	wakeAgent: (agentId: string, req?: Schemas['WakeAgentRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['WakeAgentResponse']>('POST', `/api/v1/agents/${enc(agentId)}/wake`, req, opts),
	listAgentRuns: (agentId: string, q?: operations['listAgentRuns']['parameters']['query']) =>
		apiRequest<Schemas['RunListResponse']>('GET', `/api/v1/agents/${enc(agentId)}/runs${buildQuery(q)}`),
	getAgentNotes: (agentId: string) =>
		apiRequest<Schemas['AgentNotesResponse']>('GET', `/api/v1/agents/${enc(agentId)}/notes`),

	listTeams: (q?: operations['listTeams']['parameters']['query']) =>
		apiRequest<Schemas['TeamListResponse']>('GET', `/api/v1/teams${buildQuery(q)}`),
	getTeam: (teamId: string) => apiRequest<Schemas['Team']>('GET', `/api/v1/teams/${enc(teamId)}`),
	updateTeam: (teamId: string, req: Schemas['UpdateTeamRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['Team']>('PATCH', `/api/v1/teams/${enc(teamId)}`, req, opts),
	listTemplates: (q?: operations['listTemplates']['parameters']['query']) =>
		apiRequest<Schemas['TemplateListResponse']>('GET', `/api/v1/templates${buildQuery(q)}`),

	listGoals: (q?: operations['listGoals']['parameters']['query']) =>
		apiRequest<Schemas['GoalListResponse']>('GET', `/api/v1/goals${buildQuery(q)}`),
	createGoal: (req: Schemas['CreateGoalRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['Goal']>('POST', '/api/v1/goals', req, opts),

	listTasks: (q?: operations['listTasks']['parameters']['query']) =>
		apiRequest<Schemas['TaskListResponse']>('GET', `/api/v1/tasks${buildQuery(q)}`),
	getTask: (taskId: string) => apiRequest<Schemas['Task']>('GET', `/api/v1/tasks/${enc(taskId)}`),
	getTaskTree: (taskId: string) =>
		apiRequest<Schemas['TaskTreeNode']>('GET', `/api/v1/tasks/${enc(taskId)}/tree`),
	createTask: (req: Schemas['CreateTaskRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['Task']>('POST', '/api/v1/tasks', req, opts),
	updateTask: (taskId: string, req: Schemas['UpdateTaskRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['Task']>('PATCH', `/api/v1/tasks/${enc(taskId)}`, req, opts),
	commentTask: (taskId: string, req: Schemas['CommentTaskRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['TaskEvent']>('POST', `/api/v1/tasks/${enc(taskId)}/comments`, req, opts),
	listTaskEvents: (taskId: string, q?: operations['listTaskEvents']['parameters']['query']) =>
		apiRequest<Schemas['TaskEventListResponse']>('GET', `/api/v1/tasks/${enc(taskId)}/events${buildQuery(q)}`),

	listRoutines: (q?: operations['listRoutines']['parameters']['query']) =>
		apiRequest<Schemas['RoutineListResponse']>('GET', `/api/v1/routines${buildQuery(q)}`),
	upsertRoutine: (slug: string, req: Schemas['UpsertRoutineRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['Routine']>('PUT', `/api/v1/routines/${enc(slug)}`, req, opts),
	setRoutineEnabled: (slug: string, enabled: boolean, opts?: RequestOptions) =>
		apiRequest<Schemas['Routine']>('POST', `/api/v1/routines/${enc(slug)}/enabled`, { enabled }, opts),
	runRoutineNow: (slug: string, opts?: RequestOptions) =>
		apiRequest<Schemas['RunRoutineNowResponse']>('POST', `/api/v1/routines/${enc(slug)}/run`, undefined, opts),

	setupTotp: (opts?: RequestOptions) =>
		apiRequest<Schemas['TotpSetupResponse']>('POST', '/api/v1/auth/totp/setup', undefined, opts),
	confirmTotp: (code: string, opts?: RequestOptions) =>
		apiRequest<Schemas['UserResponse']>('POST', '/api/v1/auth/totp/confirm', { code }, opts),

	listFacts: () => apiRequest<Schemas['FactListResponse']>('GET', '/api/v1/knowledge/facts'),
	undoLastLearning: (opts?: RequestOptions) =>
		apiRequest<Schemas['UndoLearnResponse']>('POST', '/api/v1/knowledge/learn/undo', undefined, opts),

	listProposals: (q?: operations['listProposals']['parameters']['query']) =>
		apiRequest<Schemas['ProposalListResponse']>('GET', `/api/v1/proposals${buildQuery(q)}`),
	decideProposal: (proposalId: string, approve: boolean, opts?: RequestOptions) =>
		apiRequest<Schemas['Proposal']>('POST', `/api/v1/proposals/${enc(proposalId)}/decide`, { approve }, opts),

	listConnections: () => apiRequest<Schemas['ConnectionListResponse']>('GET', '/api/v1/connections'),
	upsertConnection: (name: string, req: Schemas['UpsertConnectionRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['ConnectionItem']>('PUT', `/api/v1/connections/${enc(name)}`, req, opts),
	deleteConnection: (name: string, opts?: RequestOptions) =>
		apiRequest<void>('DELETE', `/api/v1/connections/${enc(name)}`, undefined, opts),

	listAgentGrants: (agentId: string) =>
		apiRequest<Schemas['GrantListResponse']>('GET', `/api/v1/agents/${enc(agentId)}/grants`),
	createAgentGrant: (agentId: string, req: Schemas['CreateGrantRequest'], opts?: RequestOptions) =>
		apiRequest<Schemas['GrantItem']>('POST', `/api/v1/agents/${enc(agentId)}/grants`, req, opts),
	revokeGrant: (grantId: string, opts?: RequestOptions) =>
		apiRequest<void>('DELETE', `/api/v1/grants/${enc(grantId)}`, undefined, opts)
};
