<script lang="ts">
	import { ApiError } from '#lib/api/client.js';
	import RoutineForm, { type RoutineSubmitPayload } from '#lib/components/RoutineForm.svelte';
	import { t } from '#lib/i18n/vi.js';
	import { routinesState } from '#lib/state/routines.svelte.js';
	import { Button, Card, EmptyState, Skeleton, StatusBadge, toast } from '#lib/ui/index.js';

	let saving = $state(false);

	$effect(() => {
		void routinesState.load();
	});

	async function saveRoutine(payload: RoutineSubmitPayload): Promise<void> {
		saving = true;
		try {
			await routinesState.upsert(payload.slug, {
				agent: payload.agent,
				schedule: payload.schedule,
				origin: payload.origin,
				catchUp: payload.catchUp,
				enabled: payload.enabled,
				body: payload.body
			});
			toast.show({ kind: 'info', messageKey: `Đã lưu routine ${payload.slug}` });
		} finally {
			saving = false;
		}
	}

	async function handleRunNow(slug: string): Promise<void> {
		try {
			const res = await routinesState.runNow(slug);
			toast.show({ kind: 'info', messageKey: `Đã kích hoạt routine (task ${res.taskId})` });
		} catch (err) {
			if (err instanceof ApiError && err.status === 409) {
				toast.show({
					kind: 'error',
					messageKey: `Lượt trước đang chạy (${err.activeRunId ?? err.activeTaskId ?? ''})`
				});
			} else {
				toast.show({
					kind: 'error',
					messageKey: err instanceof Error ? err.message : 'Không chạy được routine'
				});
			}
		}
	}
</script>

<div class="flex flex-col gap-4">
	<h1 class="text-lg font-semibold text-foreground">{t('routines.title')}</h1>

	<RoutineForm
		agents={routinesState.agents.map((a) => ({ slug: a.slug, name: a.name }))}
		{saving}
		onsubmit={saveRoutine}
	/>

	<Card title="Danh sách Routine">
		{#if routinesState.loading && routinesState.routines.length === 0}
			<Skeleton lines={4} />
		{:else if routinesState.routines.length === 0}
			<EmptyState message={t('common.empty')} />
		{:else}
			<ul class="divide-y divide-border">
				{#each routinesState.routines as rt (rt.slug)}
					<li class="flex flex-wrap items-center justify-between gap-3 py-3">
						<div class="flex flex-col gap-1">
							<div class="flex items-center gap-2">
								<span class="font-mono text-sm font-semibold text-foreground">{rt.slug}</span>
								<StatusBadge
									kind="agent"
									status={rt.enabled && !rt.autoDisabled ? 'active' : 'paused'}
								/>
								{#if rt.origin === 'inbound'}
									<span class="rounded-[var(--radius-full)] bg-warning-bg px-2 py-0.5 text-xs font-medium text-warning">
										inbound
									</span>
								{/if}
							</div>
							<p class="font-mono text-xs text-muted">
								{rt.agent} · {rt.schedule ?? rt.at ?? ''}
							</p>
						</div>

						<div class="flex items-center gap-2">
							<Button variant="outline" onclick={() => void handleRunNow(rt.slug)}>
								{t('routines.run_now')}
							</Button>
							<Button
								variant="ghost"
								onclick={() => void routinesState.toggleEnabled(rt.slug, !rt.enabled)}
							>
								{rt.enabled ? t('agents.pause') : t('agents.resume')}
							</Button>
						</div>
					</li>
				{/each}
			</ul>
		{/if}
	</Card>
</div>
