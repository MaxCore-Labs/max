<script lang="ts">
	import TaskBoard from '#lib/components/TaskBoard.svelte';
	import { t } from '#lib/i18n/vi.js';
	import { tasksState } from '#lib/state/tasks.svelte.js';
	import {
		Button,
		Card,
		Dialog,
		Field,
		Input,
		Select,
		Skeleton,
		Textarea,
		toast
	} from '#lib/ui/index.js';

	let createOpen = $state(false);
	let newTitle = $state('');
	let newDesc = $state('');
	let newAssignee = $state('');
	let creating = $state(false);

	$effect(() => {
		void tasksState.loadInitial();
	});

	async function submitCreate(): Promise<void> {
		if (!newTitle.trim()) return;
		creating = true;
		try {
			await tasksState.createTask({
				title: newTitle.trim(),
				description: newDesc.trim(),
				assigneeAgentId: newAssignee || undefined
			});
			newTitle = '';
			newDesc = '';
			createOpen = false;
			toast.show({ kind: 'info', messageKey: 'Đã tạo công việc mới' });
		} finally {
			creating = false;
		}
	}
</script>

<div class="flex flex-col gap-4">
	<div class="flex flex-wrap items-center justify-between gap-3">
		<h1 class="text-lg font-semibold text-foreground">{t('tasks.title')}</h1>
		<div class="flex items-center gap-2">
			<a
				href="/routines"
				class="inline-flex h-10 items-center rounded-[var(--radius)] border border-border-strong bg-surface px-4 text-sm font-medium text-foreground no-underline hover:bg-button-hover"
			>
				{t('nav.routines')}
			</a>
			<Button variant="primary" onclick={() => (createOpen = true)}>{t('tasks.new')}</Button>
		</div>
	</div>

	<Card>
		<div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
			<Field label="Team">
				<Select
					value={tasksState.filters.teamId}
					options={[
						{ value: '', label: t('agents.all_teams') },
						...tasksState.teams.map((tm) => ({ value: tm.id, label: tm.name }))
					]}
					onchange={(ev) =>
						void tasksState.setFilter({ teamId: (ev.currentTarget as HTMLSelectElement).value })}
				/>
			</Field>
			<Field label="Người nhận">
				<Select
					value={tasksState.filters.assigneeId}
					options={[
						{ value: '', label: 'Mọi agent' },
						...tasksState.agents.map((ag) => ({ value: ag.id, label: ag.name }))
					]}
					onchange={(ev) =>
						void tasksState.setFilter({
							assigneeId: (ev.currentTarget as HTMLSelectElement).value
						})}
				/>
			</Field>
			<Field label="Mục tiêu">
				<Select
					value={tasksState.filters.goalId}
					options={[
						{ value: '', label: 'Mọi mục tiêu' },
						...tasksState.goals.map((g) => ({ value: g.id, label: g.title }))
					]}
					onchange={(ev) =>
						void tasksState.setFilter({ goalId: (ev.currentTarget as HTMLSelectElement).value })}
				/>
			</Field>
		</div>
	</Card>

	{#if tasksState.loading && tasksState.tasks.length === 0}
		<Skeleton lines={6} />
	{:else if tasksState.error}
		<Card><p class="text-sm text-error-text">{tasksState.error}</p></Card>
	{:else}
		<TaskBoard tasks={tasksState.tasks} />
		{#if tasksState.nextCursor}
			<div class="flex justify-center">
				<Button
					variant="outline"
					loading={tasksState.loadingMore}
					onclick={() => void tasksState.loadMore()}
				>
					Tải thêm
				</Button>
			</div>
		{/if}
	{/if}
</div>

<Dialog bind:open={createOpen} title={t('tasks.new')} busy={creating}>
	<div class="flex flex-col gap-3 text-foreground">
		<Field label="Tiêu đề">
			<Input bind:value={newTitle} placeholder="Ra mắt chiến dịch mới…" />
		</Field>
		<Field label="Giao cho agent">
			<Select
				bind:value={newAssignee}
				options={[
					{ value: '', label: 'Chủ nhân giữ' },
					...tasksState.agents.map((ag) => ({ value: ag.id, label: ag.name }))
				]}
			/>
		</Field>
		<Field label="Mô tả chi tiết">
			<Textarea rows={3} bind:value={newDesc} />
		</Field>
	</div>
	{#snippet actions()}
		<Button variant="outline" onclick={() => (createOpen = false)}>{t('common.cancel')}</Button>
		<Button variant="primary" loading={creating} onclick={() => void submitCreate()}>
			{t('common.save')}
		</Button>
	{/snippet}
</Dialog>
