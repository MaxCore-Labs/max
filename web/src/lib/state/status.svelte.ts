import { api, type Schemas } from '#lib/api/client.js';

export class SystemStatusState {
	status = $state<Schemas['StatusResponse'] | null>(null);
	settings = $state<Schemas['SettingsResponse'] | null>(null);
	loading = $state(false);

	load = async (): Promise<void> => {
		this.loading = true;
		try {
			const [st, cfg] = await Promise.all([api.getStatus(), api.getSettings()]);
			this.status = st;
			this.settings = cfg;
		} finally {
			this.loading = false;
		}
	};

	saveSettings = async (req: Schemas['UpdateSettingsRequest']): Promise<void> => {
		this.settings = await api.updateSettings(req);
		this.status = await api.getStatus();
	};
}

export const systemStatus = new SystemStatusState();
