import { SvelteMap, SvelteSet } from 'svelte/reactivity';
import { api, type Schemas } from '#lib/api/client.js';

export type ActivityTimeRange = '1h' | '24h' | '7d' | '30d';

const RANGE_MS: Record<ActivityTimeRange, number> = {
	'1h': 3600_000,
	'24h': 24 * 3600_000,
	'7d': 7 * 24 * 3600_000,
	'30d': 30 * 24 * 3600_000
};

export class RunsState {
	runs = $state<Schemas['Run'][]>([]);
	nextCursor = $state<string | null>(null);
	loading = $state(false);
	loadingMore = $state(false);
	error = $state('');
	timeRange = $state<ActivityTimeRange>('24h');
	statusFilter = $state('');
	agentFilter = $state('');
	eventsByRun = new SvelteMap<string, Schemas['RunEvent'][]>();

	load = async (): Promise<void> => {
		this.loading = true;
		this.error = '';
		try {
			const res = await api.listRuns(this.buildQuery());
			this.runs = res.items;
			this.nextCursor = res.nextCursor ?? null;
		} catch (err) {
			this.error = err instanceof Error ? err.message : 'Không tải được hoạt động';
		} finally {
			this.loading = false;
		}
	};

	loadMore = async (): Promise<void> => {
		if (!this.nextCursor || this.loadingMore || this.loading) return;
		this.loadingMore = true;
		try {
			const res = await api.listRuns({ ...this.buildQuery(), cursor: this.nextCursor });
			const seen = new SvelteSet(this.runs.map((r) => r.id));
			this.runs = [...this.runs, ...res.items.filter((r) => !seen.has(r.id))];
			this.nextCursor = res.nextCursor ?? null;
		} finally {
			this.loadingMore = false;
		}
	};

	setTimeRange = async (range: ActivityTimeRange): Promise<void> => {
		this.timeRange = range;
		await this.load();
	};

	setStatus = async (status: string): Promise<void> => {
		this.statusFilter = status;
		await this.load();
	};

	loadRunEvents = async (runId: string): Promise<Schemas['RunEvent'][]> => {
		const cached = this.eventsByRun.get(runId);
		if (cached) return cached;
		const res = await api.listRunEvents(runId, { limit: 100 });
		this.eventsByRun.set(runId, res.items);
		return res.items;
	};

	private buildQuery(): {
		limit: number;
		since: number;
		status?: string;
		agentId?: string;
	} {
		const windowMs = RANGE_MS[this.timeRange] ?? RANGE_MS['24h'];
		return {
			limit: 50,
			since: Date.now() - windowMs,
			status: this.statusFilter || undefined,
			agentId: this.agentFilter || undefined
		};
	}
}

export const runsState = new RunsState();
