<script lang="ts">
	import { api, type Schemas } from '#lib/api/client.js';
	import FactRow from '#lib/components/FactRow.svelte';
	import ProposalDiff from '#lib/components/ProposalDiff.svelte';
	import SkillProposalCard from '#lib/components/SkillProposalCard.svelte';
	import { t } from '#lib/i18n/vi.js';
	import { Button, Card, EmptyState, Skeleton, toast } from '#lib/ui/index.js';

	let facts = $state<Schemas['FactItem'][]>([]);
	let proposals = $state<Schemas['Proposal'][]>([]);
	let loading = $state(true);
	let busy = $state(false);

	async function loadAll(): Promise<void> {
		loading = true;
		try {
			const [fRes, pRes] = await Promise.all([
				api.listFacts(),
				api.listProposals({ status: 'pending' })
			]);
			facts = fRes.items;
			proposals = pRes.items;
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		void loadAll();
	});

	async function undoLast(): Promise<void> {
		busy = true;
		try {
			await api.undoLastLearning();
			toast.show({ kind: 'info', messageKey: 'Đã hoàn tác lượt học gần nhất' });
			await loadAll();
		} catch (err) {
			toast.show({
				kind: 'error',
				messageKey: err instanceof Error ? err.message : 'Không thể hoàn tác'
			});
		} finally {
			busy = false;
		}
	}

	async function decide(id: string, approve: boolean): Promise<void> {
		busy = true;
		try {
			await api.decideProposal(id, approve);
			await loadAll();
		} catch (err) {
			toast.show({
				kind: 'error',
				messageKey: err instanceof Error ? err.message : 'Không xử lý được đề xuất'
			});
		} finally {
			busy = false;
		}
	}

	function parsePayload(raw: string): Record<string, string> {
		try {
			return JSON.parse(raw) as Record<string, string>;
		} catch {
			return {};
		}
	}
</script>

<div class="mx-auto flex max-w-4xl flex-col gap-6">
	<div class="flex flex-wrap items-center justify-between gap-3">
		<h1 class="text-xl font-semibold text-foreground">{t('knowledge.title')}</h1>
		<Button variant="outline" loading={busy} onclick={() => void undoLast()}>
			{t('knowledge.undo_last')}
		</Button>
	</div>

	{#if loading}
		<Card><Skeleton lines={5} /></Card>
	{:else}
		{#if proposals.length > 0}
			<section class="flex flex-col gap-3">
				<h2 class="text-sm font-semibold text-foreground">{t('knowledge.proposals')}</h2>
				{#each proposals as prop (prop.id)}
					{@const data = parsePayload(prop.payload)}
					{#if prop.kind === 'instruction_edit'}
						<ProposalDiff
							title={prop.title}
							targetPath={prop.targetPath ?? data.target_path ?? ''}
							diff={data.diff ?? ''}
							loading={busy}
							ondecide={(ok) => void decide(prop.id, ok)}
						/>
					{:else if prop.kind === 'skill'}
						<SkillProposalCard
							title={prop.title}
							slug={data.slug ?? ''}
							description={data.description ?? ''}
							loading={busy}
							ondecide={(ok) => void decide(prop.id, ok)}
						/>
					{:else}
						<Card title={prop.title}>
							<div class="flex items-center justify-between gap-3 text-sm">
								<span class="font-mono text-xs text-muted">{prop.kind}</span>
								<div class="flex gap-2">
									<Button variant="outline" onclick={() => void decide(prop.id, false)}>Từ chối</Button>
									<Button variant="primary" onclick={() => void decide(prop.id, true)}>Duyệt</Button>
								</div>
							</div>
						</Card>
					{/if}
				{/each}
			</section>
		{/if}

		<Card title={t('knowledge.facts')}>
			{#if facts.length === 0}
				<EmptyState message={t('common.empty')} />
			{:else}
				<div class="divide-y divide-border">
					{#each facts as fact (fact.slug)}
						<FactRow {fact} />
					{/each}
				</div>
			{/if}
		</Card>
	{/if}
</div>
