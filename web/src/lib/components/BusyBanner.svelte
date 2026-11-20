<script lang="ts">
	import { api, type Schemas } from '#lib/api/client.js';
	import { t } from '#lib/i18n/vi.js';
	import { Button, toast } from '#lib/ui/index.js';

	interface Props {
		busy: Schemas['BusyInfo'];
		onstopped?: () => void;
		class?: string;
	}

	let { busy, onstopped, class: className = '' }: Props = $props();
	let stopping = $state(false);

	async function stopBusy(): Promise<void> {
		if (stopping) return;
		stopping = true;
		try {
			await api.cancelRun(busy.runId, { mode: 'requeue' });
			onstopped?.();
		} catch (err) {
			toast.show({
				kind: 'error',
				messageKey: err instanceof Error ? err.message : 'Không dừng được lượt chạy'
			});
		} finally {
			stopping = false;
		}
	}
</script>

<div
	class="flex flex-wrap items-center justify-between gap-3 rounded-[var(--radius)] border border-border-strong bg-surface px-4 py-2.5 text-sm {className}"
>
	<span class="text-muted">
		{t('chat.busy_text', { task: busy.title, minutes: busy.minutesRunning })}
	</span>
	<Button variant="outline" loading={stopping} onclick={stopBusy}>
		{t('chat.stop_busy')}
	</Button>
</div>
