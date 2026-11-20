<script lang="ts">
	import { api, type Schemas } from '#lib/api/client.js';
	import ConnectionForm from '#lib/components/ConnectionForm.svelte';
	import EngineSettings from '#lib/components/EngineSettings.svelte';
	import HealthDots from '#lib/components/HealthDots.svelte';
	import TotpSetup from '#lib/components/TotpSetup.svelte';
	import { t } from '#lib/i18n/vi.js';
	import { systemStatus } from '#lib/state/status.svelte.js';
	import { Button, Card, EmptyState, Skeleton, toast } from '#lib/ui/index.js';

	let conns = $state<Schemas['ConnectionItem'][]>([]);
	let me = $state<Schemas['UserResponse'] | null>(null);
	let editingConn = $state<Schemas['ConnectionItem'] | null>(null);
	let savingConn = $state(false);

	async function loadExtra(): Promise<void> {
		try {
			const [cList, user] = await Promise.all([api.listConnections(), api.getMe()]);
			conns = cList.items;
			me = user;
		} catch {
			// Ignore transient load error
		}
	}

	$effect(() => {
		void systemStatus.load();
		void loadExtra();
	});

	async function saveConnection(name: string, req: Schemas['UpsertConnectionRequest']): Promise<void> {
		savingConn = true;
		try {
			await api.upsertConnection(name, req);
			editingConn = null;
			await loadExtra();
			toast.show({ kind: 'info', messageKey: 'Đã lưu kết nối MCP' });
		} finally {
			savingConn = false;
		}
	}

	async function removeConn(name: string): Promise<void> {
		await api.deleteConnection(name);
		await loadExtra();
	}
</script>

<div class="mx-auto flex max-w-4xl flex-col gap-6">
	<h1 class="text-xl font-semibold text-foreground">{t('system.title')}</h1>

	{#if systemStatus.loading && !systemStatus.status}
		<Card><Skeleton lines={4} /></Card>
	{:else if systemStatus.status && systemStatus.settings}
		{#if systemStatus.status.warnings && systemStatus.status.warnings.length > 0}
			<div class="rounded-lg border border-border-strong bg-warning-bg p-3 text-sm text-warning">
				{#each systemStatus.status.warnings as w, idx (idx)}
					<p>{w}</p>
				{/each}
			</div>
		{/if}

		<Card title={t('system.deps')}>
			<div class="flex flex-col gap-4">
				<HealthDots engines={systemStatus.status.engines} deps={systemStatus.status.deps} />
				<div class="text-sm text-muted tabular-nums">
					Tổng việc đang xếp hàng: <b class="font-semibold text-foreground">{systemStatus.status.queue.total}</b>
				</div>
			</div>
		</Card>

		<EngineSettings
			defaultEngine={systemStatus.settings.defaultEngine}
			settings={systemStatus.settings.engines}
			statuses={systemStatus.status.engines}
			onsaved={() => void systemStatus.load()}
		/>

		<Card title={t('connections.title')}>
			<div class="flex flex-col gap-4">
				<ConnectionForm existing={editingConn} loading={savingConn} onsave={saveConnection} />
				{#if conns.length === 0}
					<EmptyState message={t('common.empty')} />
				{:else}
					<ul class="divide-y divide-border text-sm">
						{#each conns as c (c.name)}
							<li class="flex flex-wrap items-center justify-between gap-2 py-2">
								<div>
									<span class="font-mono text-xs font-semibold">{c.name}</span>
									<span class="ml-2 text-xs text-muted">
										{c.kind} · {c.configured ? 'Đã cấu hình khóa' : 'Chưa có khóa'}
									</span>
								</div>
								<div class="flex gap-2">
									<Button variant="outline" onclick={() => (editingConn = c)}>Sửa</Button>
									<Button variant="danger" onclick={() => void removeConn(c.name)}>Xoá</Button>
								</div>
							</li>
						{/each}
					</ul>
				{/if}
			</div>
		</Card>

		<TotpSetup
			enabled={me?.totpEnabled ?? false}
			onenabled={() => {
				void loadExtra();
				void systemStatus.load();
			}}
		/>
	{/if}
</div>
