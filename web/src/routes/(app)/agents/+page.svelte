<script lang="ts">
	import AgentTable from '#lib/components/AgentTable.svelte';
	import BulkCreateDialog from '#lib/components/BulkCreateDialog.svelte';
	import TeamTree from '#lib/components/TeamTree.svelte';
	import { t } from '#lib/i18n/vi.js';
	import { agentsState } from '#lib/state/agents.svelte.js';
	import { Button, Card, EmptyState, Field, Input, Select, Skeleton } from '#lib/ui/index.js';

	let bulkOpen = $state(false);
	let searchDraft = $state('');

	$effect(() => {
		void agentsState.loadInitial();
		return () => agentsState.dispose();
	});

	let hasActiveFilter = $derived(
		Boolean(
			agentsState.filters.team ||
				agentsState.filters.template ||
				agentsState.filters.status ||
				agentsState.filters.q
		)
	);

	function submitSearch(): void {
		void agentsState.setFilter({ q: searchDraft });
	}
</script>

<div class="flex flex-col gap-4">
	<div class="flex flex-wrap items-center justify-between gap-3">
		<h1 class="text-lg font-semibold text-foreground">{t('agents.title')}</h1>
		<Button variant="primary" onclick={() => (bulkOpen = true)}>
			{t('agents.bulk_create')}
		</Button>
	</div>

	<div class="grid grid-cols-1 gap-4 lg:grid-cols-[260px_1fr]">
		<Card title={t('agents.teams_heading')}>
			{#if agentsState.loading && agentsState.teams.length === 0}
				<Skeleton lines={4} />
			{:else if agentsState.teams.length === 0}
				<EmptyState message={t('common.empty')} />
			{:else}
				<TeamTree
					teams={agentsState.teams}
					agents={agentsState.agents}
					selectedTeam={agentsState.filters.team}
					onSelectTeam={(slug) => void agentsState.setFilter({ team: slug })}
				/>
			{/if}
		</Card>

		<div class="flex flex-col gap-4">
			<Card>
				<div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
					<Field label={t('agents.col.name')}>
						<Input
							bind:value={searchDraft}
							placeholder={t('agents.search_placeholder')}
							onchange={submitSearch}
						/>
					</Field>
					<Field label={t('agents.col.template')}>
						<Select
							value={agentsState.filters.template}
							options={[
								{ value: '', label: t('agents.all_templates') },
								...agentsState.templates.map((tpl) => ({ value: tpl.slug, label: tpl.name }))
							]}
							onchange={(ev) =>
								void agentsState.setFilter({
									template: (ev.currentTarget as HTMLSelectElement).value
								})}
						/>
					</Field>
					<Field label={t('agents.col.status')}>
						<Select
							value={agentsState.filters.status}
							options={[
								{ value: '', label: t('agents.all_statuses') },
								{ value: 'active', label: t('status.agent.active') },
								{ value: 'paused', label: t('status.agent.paused') },
								{ value: 'quota_paused', label: t('status.agent.quota_paused') }
							]}
							onchange={(ev) =>
								void agentsState.setFilter({
									status: (ev.currentTarget as HTMLSelectElement).value
								})}
						/>
					</Field>
				</div>
			</Card>

			<Card flush>
				{#if agentsState.loading && agentsState.agents.length === 0}
					<div class="p-5"><Skeleton lines={5} /></div>
				{:else if agentsState.error}
					<p class="p-5 text-sm text-error-text">{agentsState.error}</p>
				{:else if agentsState.agents.length === 0}
					<div class="p-5">
						<EmptyState message={t('common.empty')}>
							{#snippet action()}
								{#if hasActiveFilter}
									<button
										type="button"
										onclick={() => {
											searchDraft = '';
											void agentsState.clearFilters();
										}}
										class="cursor-pointer font-medium text-foreground underline"
									>
										{t('common.clear_search')}
									</button>
								{/if}
							{/snippet}
						</EmptyState>
					</div>
				{:else}
					<AgentTable
						agents={agentsState.agents}
						viewportHeight={440}
						onLoadMore={() => void agentsState.loadMore()}
					/>
				{/if}
			</Card>
		</div>
	</div>
</div>

<BulkCreateDialog
	bind:open={bulkOpen}
	templates={agentsState.templates}
	teams={agentsState.teams}
	oncreated={() => void agentsState.loadInitial()}
/>
