import { SvelteSet } from 'svelte/reactivity';
import { api, type Schemas } from '#lib/api/client.js';
import { events, type EventStreamManager } from './events.svelte.js';

export interface AgentListFilters {
	team: string;
	template: string;
	status: string;
	q: string;
}

export class AgentsState {
	agents = $state<Schemas['Agent'][]>([]);
	teams = $state<Schemas['Team'][]>([]);
	templates = $state<Schemas['RoleTemplate'][]>([]);
	nextCursor = $state<string | null>(null);
	loading = $state(false);
	loadingMore = $state(false);
	error = $state('');
	filters = $state<AgentListFilters>({ team: '', template: '', status: '', q: '' });

	private stream: EventStreamManager;
	private unsub: (() => void) | null = null;

	constructor(stream: EventStreamManager = events) {
		this.stream = stream;
	}

	loadInitial = async (): Promise<void> => {
		this.loading = true;
		this.error = '';
		try {
			const [teamRes, tplRes, agRes] = await Promise.all([
				api.listTeams({ limit: 100 }),
				api.listTemplates({ limit: 100 }),
				api.listAgents(this.buildQuery())
			]);
			this.teams = teamRes.items;
			this.templates = tplRes.items;
			this.agents = agRes.items;
			this.nextCursor = agRes.nextCursor ?? null;
			this.bindTeamTopics();
		} catch (err) {
			this.error = err instanceof Error ? err.message : 'Không tải được danh sách agent';
		} finally {
			this.loading = false;
		}
	};

	reloadAgents = async (): Promise<void> => {
		this.loading = true;
		this.error = '';
		try {
			const agRes = await api.listAgents(this.buildQuery());
			this.agents = agRes.items;
			this.nextCursor = agRes.nextCursor ?? null;
		} catch (err) {
			this.error = err instanceof Error ? err.message : 'Không lọc được agent';
		} finally {
			this.loading = false;
		}
	};

	loadMore = async (): Promise<void> => {
		if (!this.nextCursor || this.loadingMore || this.loading) return;
		this.loadingMore = true;
		try {
			const agRes = await api.listAgents({ ...this.buildQuery(), cursor: this.nextCursor });
			const existing = new SvelteSet(this.agents.map((a) => a.id));
			const appended = agRes.items.filter((a) => !existing.has(a.id));
			this.agents = [...this.agents, ...appended];
			this.nextCursor = agRes.nextCursor ?? null;
		} finally {
			this.loadingMore = false;
		}
	};

	setFilter = async (patch: Partial<AgentListFilters>): Promise<void> => {
		this.filters = { ...this.filters, ...patch };
		await this.reloadAgents();
	};

	clearFilters = async (): Promise<void> => {
		this.filters = { team: '', template: '', status: '', q: '' };
		await this.reloadAgents();
	};

	dispose = (): void => {
		this.unsub?.();
		this.unsub = null;
	};

	private buildQuery(): {
		limit: number;
		team?: string;
		template?: string;
		status?: string;
		q?: string;
	} {
		return {
			limit: 50,
			team: this.filters.team || undefined,
			template: this.filters.template || undefined,
			status: this.filters.status || undefined,
			q: this.filters.q.trim() || undefined
		};
	}

	private bindTeamTopics(): void {
		this.unsub?.();
		const topics = this.teams.map((tm) => `team:${tm.id}`);
		if (topics.length === 0) return;
		this.unsub = this.stream.subscribe(
			topics,
			(ev) => {
				if (!ev.topic.startsWith('team:')) return;
				const teamId = ev.topic.slice('team:'.length);
				const payload = (ev.data ?? {}) as { agentCount?: number; running?: number };
				this.teams = this.teams.map((tm) => {
					if (tm.id !== teamId) return tm;
					return {
						...tm,
						agentCount: payload.agentCount ?? tm.agentCount,
						running: payload.running ?? tm.running
					};
				});
			},
			() => this.loadInitial()
		);
	}
}

export const agentsState = new AgentsState();
