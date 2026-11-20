<script lang="ts">
	import { page } from '$app/state';
	import { api, type Schemas } from '#lib/api/client.js';
	import MarkdownView from '#lib/components/MarkdownView.svelte';
	import TaskThread from '#lib/components/TaskThread.svelte';
	import TaskTree from '#lib/components/TaskTree.svelte';
	import { t } from '#lib/i18n/vi.js';
	import { Button, Card, EmptyState, Skeleton, StatusBadge, Textarea, toast } from '#lib/ui/index.js';

	let task = $state<Schemas['Task'] | null>(null);
	let tree = $state<Schemas['TaskTreeNode'] | null>(null);
	let taskEvents = $state<Schemas['TaskEvent'][]>([]);
	let commentBody = $state('');
	let loading = $state(true);
	let sending = $state(false);
	let error = $state('');

	let taskId = $derived(page.params.id ?? '');

	async function loadTask(id: string): Promise<void> {
		if (!id) return;
		loading = true;
		error = '';
		try {
			const [tk, tr, evs] = await Promise.all([
				api.getTask(id),
				api.getTaskTree(id),
				api.listTaskEvents(id, { limit: 100 })
			]);
			task = tk;
			tree = tr;
			taskEvents = evs.items;
		} catch (err) {
			error = err instanceof Error ? err.message : 'Không tải được chi tiết việc';
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		void loadTask(taskId);
	});

	async function submitComment(): Promise<void> {
		if (!task || !commentBody.trim()) return;
		sending = true;
		try {
			const ev = await api.commentTask(task.id, { body: commentBody.trim() });
			taskEvents = [...taskEvents, ev];
			commentBody = '';
			toast.show({ kind: 'info', messageKey: 'Đã gửi bình luận' });
		} finally {
			sending = false;
		}
	}
</script>

{#if loading && !task}
	<Skeleton lines={5} />
{:else if error}
	<Card><p class="text-sm text-error-text">{error}</p></Card>
{:else if task}
	<div class="flex flex-col gap-4">
		<Card>
			<div class="flex flex-wrap items-start justify-between gap-3">
				<div>
					<h1 class="text-lg font-semibold text-foreground">{task.title}</h1>
					<p class="font-mono text-xs text-muted">
						Nguồn gốc: {task.rootOrigin} · Tầng {task.depth}
					</p>
				</div>
				<StatusBadge
					kind="task"
					status={task.status}
					reason={task.blockedReason ?? undefined}
				/>
			</div>
			{#if task.description}
				<div class="mt-3 border-t border-border pt-3">
					<MarkdownView source={task.description} class="text-sm" />
				</div>
			{/if}
		</Card>

		<div class="grid grid-cols-1 gap-4 lg:grid-cols-[320px_1fr]">
			<div class="flex flex-col gap-4">
				<Card title={t('tasks.subtasks')}>
					{#if tree}
						<TaskTree root={tree} />
					{:else}
						<EmptyState message={t('common.empty')} />
					{/if}
				</Card>

				<Card title="Sản phẩm (Artifacts)">
					{#if task.artifacts.length === 0}
						<EmptyState message={t('common.empty')} />
					{:else}
						<ul class="space-y-1 font-mono text-xs text-foreground">
							{#each task.artifacts as art (art)}
								<li class="truncate">{art}</li>
							{/each}
						</ul>
					{/if}
				</Card>
			</div>

			<Card title={t('tasks.thread')}>
				<div class="flex flex-col gap-4">
					<TaskThread taskId={task.id} initialEvents={taskEvents} />

					<div class="flex flex-col gap-2 border-t border-border pt-3">
						<Textarea
							rows={3}
							bind:value={commentBody}
							placeholder={t('tasks.comment_placeholder')}
						/>
						<div class="flex justify-end">
							<Button
								variant="primary"
								loading={sending}
								disabled={!commentBody.trim()}
								onclick={() => void submitComment()}
							>
								{t('common.send')}
							</Button>
						</div>
					</div>
				</div>
			</Card>
		</div>
	</div>
{/if}
