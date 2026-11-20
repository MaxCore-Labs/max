<script lang="ts">
	import ApprovalCard from '#lib/components/ApprovalCard.svelte';
	import { t } from '#lib/i18n/vi.js';
	import { approvals } from '#lib/state/approvals.svelte.js';
	import { inbox } from '#lib/state/inbox.svelte.js';
	import { Button, Card, EmptyState, Field, Input, Skeleton, StatusBadge } from '#lib/ui/index.js';

	let replyDrafts = $state<Record<string, string>>({});

	$effect(() => {
		void approvals.load('pending');
		void inbox.load();
	});

	async function submitReply(itemId: string, text?: string): Promise<void> {
		const ans = (text ?? replyDrafts[itemId] ?? '').trim();
		if (!ans) return;
		await inbox.answer(itemId, ans);
		replyDrafts[itemId] = '';
	}
</script>

<div class="mx-auto flex max-w-4xl flex-col gap-6">
	<h1 class="text-xl font-semibold text-foreground">{t('inbox.title')}</h1>

	{#if approvals.loading && inbox.loading && approvals.items.length === 0 && inbox.items.length === 0}
		<Card><Skeleton lines={4} /></Card>
	{:else if approvals.items.length === 0 && inbox.items.length === 0}
		<Card><EmptyState message={t('inbox.empty')} /></Card>
	{:else}
		{#each approvals.items as ap (ap.id)}
			<ApprovalCard approval={ap} ondecided={() => void approvals.load('pending')} />
		{/each}

		{#each inbox.items as item (item.id)}
			<Card title={item.title}>
				{#snippet action()}
					{#if item.kind === 'owner_question' && !item.resolvedAt}
						<StatusBadge kind="approval" status="owner_question" />
					{:else if !item.readAt}
						<Button variant="outline" onclick={() => void inbox.markRead(item.id)}>
							{t('inbox.mark_read')}
						</Button>
					{/if}
				{/snippet}

				<p class="text-sm whitespace-pre-wrap text-foreground">{item.body}</p>

				{#if item.kind === 'owner_question' && !item.resolvedAt}
					<div class="mt-4 flex flex-col gap-3">
						{#if item.choices.length > 0}
							<div class="flex flex-wrap gap-2">
								{#each item.choices as choice (choice)}
									<Button variant="outline" onclick={() => void submitReply(item.id, choice)}>
										{choice}
									</Button>
								{/each}
							</div>
						{/if}
						<div class="flex flex-wrap items-end gap-2">
							<div class="min-w-0 flex-1">
								<Field label={t('inbox.answer_label')}>
									<Input
										bind:value={replyDrafts[item.id]}
										placeholder={t('inbox.answer_placeholder')}
									/>
								</Field>
							</div>
							<Button variant="primary" onclick={() => void submitReply(item.id)}>
								{t('common.reply')}
							</Button>
						</div>
					</div>
				{/if}
			</Card>
		{/each}
	{/if}
</div>
