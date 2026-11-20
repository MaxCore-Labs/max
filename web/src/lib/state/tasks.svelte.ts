import { SvelteSet } from 'svelte/reactivity';
import { api, type Schemas } from '#lib/api/client.js';

export interface TaskFilters {
	status: string;
	teamId: string;
	assigneeId: string;
	goalId: string;
}

export class TasksState {
	tasks = $state<Schemas['Task'][]>([]);
	teams = $state<Schemas['Team'][]>([]);
	agents = $state<Schemas['Agent'][]>([]);
	goals = $state<Schemas['Goal'][]>([]);
	nextCursor = $state<string | null>(null);
	loading = $state(false);
	loadingMore = $state(false);
	error = $state('');
	filters = $state<TaskFilters>({ status: '', teamId: '', assigneeId: '', goalId: '' });

	loadInitial = async (): Promise<void> => {
		this.loading = true;
		this.error = '';
		try {
			const [tkRes, tmRes, agRes, glRes] = await Promise.all([
				api.listTasks(this.buildQuery()),
				api.listTeams({ limit: 100 }),
				api.listAgents({ limit: 100 }),
				api.listGoals({ limit: 100 })
			]);
			this.tasks = tkRes.items;
			this.nextCursor = tkRes.nextCursor ?? null;
			this.teams = tmRes.items;
			this.agents = agRes.items;
			this.goals = glRes.items;
		} catch (err) {
			this.error = err instanceof Error ? err.message : 'Không tải được danh sách việc';
		} finally {
			this.loading = false;
		}
	};

	reloadTasks = async (): Promise<void> => {
		this.loading = true;
		this.error = '';
		try {
			const tkRes = await api.listTasks(this.buildQuery());
			this.tasks = tkRes.items;
			this.nextCursor = tkRes.nextCursor ?? null;
		} catch (err) {
			this.error = err instanceof Error ? err.message : 'Không lọc được danh sách việc';
		} finally {
			this.loading = false;
		}
	};

	loadMore = async (): Promise<void> => {
		if (!this.nextCursor || this.loadingMore || this.loading) return;
		this.loadingMore = true;
		try {
			const tkRes = await api.listTasks({ ...this.buildQuery(), cursor: this.nextCursor });
			const seen = new SvelteSet(this.tasks.map((tk) => tk.id));
			this.tasks = [...this.tasks, ...tkRes.items.filter((tk) => !seen.has(tk.id))];
			this.nextCursor = tkRes.nextCursor ?? null;
		} finally {
			this.loadingMore = false;
		}
	};

	setFilter = async (patch: Partial<TaskFilters>): Promise<void> => {
		this.filters = { ...this.filters, ...patch };
		await this.reloadTasks();
	};

	createTask = async (req: Schemas['CreateTaskRequest']): Promise<Schemas['Task']> => {
		const created = await api.createTask(req);
		this.tasks = [created, ...this.tasks];
		return created;
	};

	private buildQuery(): {
		limit: number;
		status?: string;
		teamId?: string;
		assigneeId?: string;
		goalId?: string;
	} {
		return {
			limit: 50,
			status: this.filters.status || undefined,
			teamId: this.filters.teamId || undefined,
			assigneeId: this.filters.assigneeId || undefined,
			goalId: this.filters.goalId || undefined
		};
	}
}

export const tasksState = new TasksState();
