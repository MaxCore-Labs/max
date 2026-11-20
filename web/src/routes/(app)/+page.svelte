<script lang="ts">
	import ApprovalCard from '#lib/components/ApprovalCard.svelte';
	import BusyBanner from '#lib/components/BusyBanner.svelte';
	import Composer from '#lib/components/Composer.svelte';
	import MessageBubble from '#lib/components/MessageBubble.svelte';
	import ThreadList from '#lib/components/ThreadList.svelte';
	import { t } from '#lib/i18n/vi.js';
	import { threads } from '#lib/state/threads.svelte.js';
	import { Button, Card, EmptyState, Skeleton } from '#lib/ui/index.js';

	$effect(() => {
		void threads.loadList();
		return () => threads.dispose();
	});
</script>

<div class="grid grid-cols-1 gap-4 lg:grid-cols-[260px_1fr]">
	<Card>
		<ThreadList
			items={threads.list}
			activeId={threads.activeId}
			onselect={(id) => void threads.selectThread(id)}
			oncreate={() => void threads.createThread()}
		/>
	</Card>

	<Card class="flex min-h-[70vh] flex-col justify-between gap-4">
		<div class="flex items-center justify-between gap-2 border-b border-border pb-3">
			<h1 class="text-base font-semibold text-foreground">{t('nav.chat')}</h1>
			{#if threads.activeId}
				<Button variant="ghost" onclick={() => void threads.resetActive()}>
					{t('common.reset')}
				</Button>
			{/if}
		</div>

		{#if threads.busyWith}
			<BusyBanner busy={threads.busyWith} onstopped={() => void threads.reloadActive(false)} />
		{/if}

		<div class="flex flex-1 flex-col gap-3 overflow-y-auto py-2">
			{#if threads.loading && threads.messages.length === 0}
				<Skeleton lines={3} />
			{:else if threads.messages.length === 0 && !threads.liveText}
				<EmptyState message={t('chat.empty')} />
			{:else}
				{#each threads.messages as msg (msg.id)}
					<MessageBubble role={msg.role} content={msg.content} />
				{/each}
				{#if threads.liveText}
					<MessageBubble role="assistant" content={threads.liveText} streaming />
				{/if}
			{/if}

			{#each threads.approvals as ap (ap.id)}
				<ApprovalCard approval={ap} ondecided={() => void threads.reloadActive(false)} />
			{/each}
		</div>

		<Composer onsend={(txt) => threads.send(txt)} />
	</Card>
</div>
