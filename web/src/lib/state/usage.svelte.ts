import { api, type Schemas } from '#lib/api/client.js';

export class UsageState {
	rows = $state<Schemas['DailyUsageRow'][]>([]);
	quota = $state<Schemas['QuotaResponse'] | null>(null);
	loading = $state(false);

	load = async (days = 7): Promise<void> => {
		this.loading = true;
		try {
			const [uRes, qRes] = await Promise.all([api.getUsage({ days, limit: 100 }), api.getQuota()]);
			this.rows = uRes.items;
			this.quota = qRes;
		} finally {
			this.loading = false;
		}
	};

	updateQuota = async (req: Schemas['UpdateQuotaSettingsRequest']): Promise<void> => {
		this.quota = await api.updateQuotaSettings(req);
	};
}

export const usage = new UsageState();
