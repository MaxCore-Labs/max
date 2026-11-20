<script lang="ts">
	import { page } from '$app/state';
	import { api, type Schemas } from '#lib/api/client.js';
	import EnginePicker, { type EnginePickerValue } from '#lib/components/EnginePicker.svelte';
	import GrantForm from '#lib/components/GrantForm.svelte';
	import MarkdownView from '#lib/components/MarkdownView.svelte';
	import { t } from '#lib/i18n/vi.js';
	import { events } from '#lib/state/events.svelte.js';
	import { systemStatus } from '#lib/state/status.svelte.js';
	import { Button, Card, EmptyState, Skeleton, StatusBadge, Textarea, toast } from '#lib/ui/index.js';

	let agent = $state<Schemas['AgentDetail'] | null>(null);
	let notes = $state('');
	let runs = $state<Schemas['Run'][]>([]);
	let grants = $state<Schemas['GrantItem'][]>([]);
	let bodyDraft = $state('');
	let loading = $state(true);
	let saving = $state(false);
	let error = $state('');

	let agentId = $derived(page.params.id ?? '');

	async function loadDetail(id: string): Promise<void> {
		if (!id) return;
		loading = true;
		error = '';
		try {
			const [ag, nt, rn, gr] = await Promise.all([
				api.getAgent(id),
				api.getAgentNotes(id),
				api.listAgentRuns(id, { limit: 20 }),
				api.listAgentGrants(id)
			]);
			agent = ag;
			bodyDraft = ag.body;
			notes = nt.content;
			runs = rn.items;
			grants = gr.items;
			if (!systemStatus.settings) {
				await systemStatus.load();
			}
		} catch (err) {
			error = err instanceof Error ? err.message : 'Không tải được thông tin agent';
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		const id = agentId;
		void loadDetail(id);
		const unsub = events.subscribe([`agent:${id}`], () => void loadDetail(id));
		return () => unsub();
	});

	async function saveEngines(val: EnginePickerValue): Promise<void> {
		if (!agent) return;
		saving = true;
		try {
			agent = await api.updateAgent(agent.id, {
				engines: val.engines,
				models: val.models,
				allowUngatedEngine: val.allowUngatedEngine
			});
			toast.show({ kind: 'info', messageKey: 'Đã cập nhật engine cho agent' });
		} finally {
			saving = false;
		}
	}

	async function saveInstructions(): Promise<void> {
		if (!agent) return;
		saving = true;
		try {
			agent = await api.updateAgent(agent.id, { body: bodyDraft });
			toast.show({ kind: 'info', messageKey: 'Đã lưu chỉ dẫn vai trò' });
		} finally {
			saving = false;
		}
	}

	async function togglePause(): Promise<void> {
		if (!agent) return;
		const next =
			agent.status === 'paused' ? await api.resumeAgent(agent.id) : await api.pauseAgent(agent.id);
		agent = { ...agent, status: next.status };
	}

	async function wakeNow(): Promise<void> {
		if (!agent) return;
		await api.wakeAgent(agent.id);
		toast.show({ kind: 'info', messageKey: 'Đã gửi tín hiệu đánh thức' });
	}

	async function addGrant(req: Schemas['CreateGrantRequest']): Promise<void> {
		if (!agent) return;
		saving = true;
		try {
			const created = await api.createAgentGrant(agent.id, req);
			grants = [created, ...grants];
			toast.show({ kind: 'info', messageKey: 'Đã tạo grant cho agent' });
		} finally {
			saving = false;
		}
	}

	async function removeGrant(id: string): Promise<void> {
		await api.revokeGrant(id);
		grants = grants.filter((g) => g.id !== id);
	}
</script>

{#if loading && !agent}
	<Skeleton lines={6} />
{:else if error}
	<Card><p class="text-sm text-error-text">{error}</p></Card>
{:else if agent}
	<div class="flex flex-col gap-4">
		<Card>
			<div class="flex flex-wrap items-center justify-between gap-3">
				<div class="flex items-center gap-3">
					<div>
						<h1 class="text-lg font-semibold text-foreground">{agent.name}</h1>
						<p class="font-mono text-xs text-muted">{agent.slug}</p>
					</div>
					<StatusBadge kind="agent" status={agent.status} />
				</div>
				<div class="flex flex-wrap gap-2">
					<Button variant="outline" onclick={() => void wakeNow()}>{t('agents.wake')}</Button>
					<Button variant="outline" onclick={() => void togglePause()}>
						{agent.status === 'paused' ? t('agents.resume') : t('agents.pause')}
					</Button>
				</div>
			</div>
		</Card>

		<EnginePicker
			availableEngines={systemStatus.settings?.engines ?? [
				{ name: 'agy', enabled: true, slots: 3, defaultModel: 'default' }
			]}
			selectedEngines={agent.engines}
			models={agent.models}
			sensitive={agent.sensitive}
			browser={agent.browser}
			allowUngatedEngine={agent.allowUngatedEngine}
			{saving}
			onsave={saveEngines}
		/>

		<div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
			<Card title={t('agents.instructions')}>
				<div class="flex flex-col gap-3">
					<Textarea rows={8} bind:value={bodyDraft} class="font-mono text-xs" />
					<div class="flex justify-end">
						<Button variant="primary" loading={saving} onclick={() => void saveInstructions()}>
							{t('common.save')}
						</Button>
					</div>
				</div>
			</Card>

			<Card title={t('agents.notes')}>
				{#if notes.trim()}
					<MarkdownView source={notes} class="text-sm" />
				{:else}
					<EmptyState message={t('common.empty')} />
				{/if}
			</Card>
		</div>

		<Card title={t('agents.recent_runs')}>
			{#if runs.length === 0}
				<EmptyState message={t('common.empty')} />
			{:else}
				<ul class="divide-y divide-border">
					{#each runs as r (r.id)}
						<li class="flex flex-wrap items-center justify-between gap-2 py-2.5 text-sm">
							<div class="flex items-center gap-2">
								<StatusBadge kind="run" status={r.status} />
								<span class="font-mono text-xs">{r.engine}</span>
								<span class="truncate text-muted">{r.summary ?? r.id}</span>
							</div>
							<span class="font-mono text-xs text-muted tabular-nums">
								{r.inputTokens + r.outputTokens} tok
							</span>
						</li>
					{/each}
				</ul>
			{/if}
		</Card>

		<Card title={t('grants.title')}>
			<div class="flex flex-col gap-4">
				<GrantForm loading={saving} onsubmit={(r) => void addGrant(r)} />
				{#if grants.filter((g) => !g.revokedAt).length > 0}
					<ul class="divide-y divide-border text-sm">
						{#each grants.filter((g) => !g.revokedAt) as g (g.id)}
							<li class="flex flex-wrap items-center justify-between gap-2 py-2">
								<div>
									<span class="font-mono text-xs font-semibold">{g.connection}.{g.tool}</span>
									<span class="ml-2 text-xs text-muted">
										origins: {g.allowedRootOrigins.join(', ')}
										{#if g.maxPerDay} · đã dùng {g.usedToday}/{g.maxPerDay}/ngày{/if}
									</span>
								</div>
								<Button variant="danger" onclick={() => void removeGrant(g.id)}>
									{t('grants.revoke')}
								</Button>
							</li>
						{/each}
					</ul>
				{/if}
			</div>
		</Card>
	</div>
{/if}
