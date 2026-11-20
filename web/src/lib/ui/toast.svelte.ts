export type ToastKind = 'info' | 'error';

export interface ToastItem {
	id: number;
	kind: ToastKind;
	messageKey: string;
}

export class ToastStore {
	items = $state<ToastItem[]>([]);
	private nextId = 1;

	show = (opts: { kind: ToastKind; messageKey: string }): number => {
		const id = this.nextId++;
		this.items.push({ id, kind: opts.kind, messageKey: opts.messageKey });
		if (opts.kind === 'info') {
			setTimeout(() => {
				this.dismiss(id);
			}, 5000);
		}
		return id;
	};

	dismiss = (id: number): void => {
		this.items = this.items.filter((t) => t.id !== id);
	};
}

export const toast = new ToastStore();
