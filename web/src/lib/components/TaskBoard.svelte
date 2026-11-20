<script lang="ts">
	import type { Schemas } from '#lib/api/client.js';
	import { t } from '#lib/i18n/vi.js';
	import { Card, EmptyState, StatusBadge } from '#lib/ui/index.js';

	interface Props {
		tasks: Schemas['Task'][];
		class?: string;
	}

	let { tasks, class: className = '' }: Props = $props();

	const COLUMNS = [
		{ status: 'todo', labelKey: 'status.task.todo' },
		{ status: 'in_progress', labelKey: 'status.task.in_progress' },
		{ status: 'blocked', labelKey: 'Bị chặn' },
		{ status: 'waiting_owner', labelKey: 'status.task.waiting_owner' },
		{ status: 'in_review', labelKey: 'status.task.in_review' },
		{ status: 'done', labelKey: 'status.task.done' }
	] as const;

	function blockedReasonLabel(reason?: string | null): string {
		switch (reason) {
			case 'dependency':
				return t('tasks.blocked.dependency');
			case 'stalled':
				return t('tasks.blocked.stalled');
			case 'error':
				return t('tasks.blocked.error');
			case 'needs_owner':
				return t('tasks.blocked.needs_owner');
			default:
				return t('tasks.blocked.dependency');
		}
	}

	function tasksFor(status: string): Schemas['Task'][] {
		return tasks.filter((tk) => tk.status === status);
	}
</script>

<div class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3 {className}">
	{#each COLUMNS as col (col.status)}
		{@const colTasks = tasksFor(col.status)}
		<section data-testid="task-col-{col.status}">
			<Card title="{t(col.labelKey)} ({colTasks.length})">
				{#if colTasks.length === 0}
					<EmptyState message={t('common.empty')} />
				{:else}
					<div class="flex flex-col gap-2">
						{#each colTasks as tk (tk.id)}
							<a
								href="/tasks/{tk.id}"
								class="flex flex-col gap-1.5 rounded-[var(--radius)] border border-border-strong bg-surface p-3 no-underline hover:bg-item-hover"
							>
								<div class="flex items-start justify-between gap-2">
									<span class="text-sm font-medium text-foreground">{tk.title}</span>
									<StatusBadge
										kind="task"
										status={tk.status}
										reason={tk.blockedReason ?? undefined}
									/>
								</div>
								{#if tk.status === 'blocked'}
									<p class="text-xs text-muted">
										Lý do: {blockedReasonLabel(tk.blockedReason)}
									</p>
								{/if}
							</a>
						{/each}
					</div>
				{/if}
			</Card>
		</section>
	{/each}
</div>
