import { api, ApiError, type Schemas } from '#lib/api/client.js';

class SessionStore {
	user = $state<Schemas['UserResponse'] | null>(null);
	loading = $state(false);
	checked = $state(false);

	refresh = async (): Promise<Schemas['UserResponse'] | null> => {
		this.loading = true;
		try {
			const me = await api.getMe();
			this.user = me;
			return me;
		} catch (err) {
			if (err instanceof ApiError && err.status === 401) {
				this.user = null;
				return null;
			}
			throw err;
		} finally {
			this.loading = false;
			this.checked = true;
		}
	};

	login = async (password: string): Promise<Schemas['UserResponse']> => {
		this.loading = true;
		try {
			const me = await api.login({ password });
			this.user = me;
			this.checked = true;
			return me;
		} finally {
			this.loading = false;
		}
	};

	logout = async (): Promise<void> => {
		try {
			await api.logout();
		} finally {
			this.user = null;
			this.checked = true;
		}
	};
}

export const session = new SessionStore();
