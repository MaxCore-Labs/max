import { api, type Schemas } from '#lib/api/client.js';

export class RoutinesState {
	routines = $state<Schemas['Routine'][]>([]);
	agents = $state<Schemas['Agent'][]>([]);
	loading = $state(false);
	error = $state('');

	load = async (): Promise<void> => {
		this.loading = true;
		this.error = '';
		try {
			const [rtRes, agRes] = await Promise.all([
				api.listRoutines({ limit: 100 }),
				api.listAgents({ limit: 100 })
			]);
			this.routines = rtRes.items;
			this.agents = agRes.items;
		} catch (err) {
			this.error = err instanceof Error ? err.message : 'Không tải được danh sách routine';
		} finally {
			this.loading = false;
		}
	};

	upsert = async (
		slug: string,
		req: Schemas['UpsertRoutineRequest']
	): Promise<Schemas['Routine']> => {
		const saved = await api.upsertRoutine(slug, req);
		const exists = this.routines.some((r) => r.slug === slug);
		this.routines = exists
			? this.routines.map((r) => (r.slug === slug ? saved : r))
			: [...this.routines, saved];
		return saved;
	};

	toggleEnabled = async (slug: string, enabled: boolean): Promise<void> => {
		const updated = await api.setRoutineEnabled(slug, enabled);
		this.routines = this.routines.map((r) => (r.slug === slug ? updated : r));
	};

	runNow = async (slug: string): Promise<Schemas['RunRoutineNowResponse']> => {
		return api.runRoutineNow(slug);
	};
}

export const routinesState = new RoutinesState();
