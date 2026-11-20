import { api, type Schemas } from '#lib/api/client.js';

export class ApprovalsState {
	items = $state<Schemas['Approval'][]>([]);
	loading = $state(false);

	load = async (status = 'pending'): Promise<void> => {
		this.loading = true;
		try {
			const res = await api.listApprovals({ status, limit: 50 });
			this.items = res.items;
		} finally {
			this.loading = false;
		}
	};

	decide = async (approvalId: string, allow: boolean): Promise<Schemas['Approval']> => {
		const updated = await api.decideApproval(approvalId, { allow });
		this.items = this.items.map((a) => (a.id === approvalId ? updated : a));
		return updated;
	};
}

export const approvals = new ApprovalsState();
