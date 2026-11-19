<script lang="ts">
	import { liveEvents } from '#lib/events.svelte.js';
</script>

<section class="rounded-[var(--radius-lg)] border border-border bg-surface p-4 sm:p-5">
	<div class="flex items-center justify-between border-b border-border pb-3">
		<div>
			<h1 class="text-base font-semibold">Luồng sự kiện thời gian thực (`/api/v1/events`)</h1>
			<p class="mt-0.5 text-xs text-muted">Đúng một kết nối EventSource dùng chung qua `$state`</p>
		</div>
		{#if liveEvents.lastEvent}
			<span
				data-testid="latest-latency"
				class="rounded-[var(--radius-full)] bg-success-bg px-3 py-1 font-mono text-xs font-medium tabular-nums text-success"
			>
				{liveEvents.lastEvent.latencyMs} ms
			</span>
		{/if}
	</div>

	{#if !liveEvents.lastEvent}
		<p data-testid="empty-stream" class="py-8 text-center text-sm text-muted">
			Đang chờ sự kiện đầu tiên từ máy chủ...
		</p>
	{:else}
		<div class="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-3">
			<div class="rounded-[var(--radius)] border border-border-strong p-3">
				<div class="text-xs text-muted">Số thứ tự gần nhất</div>
				<div data-testid="latest-seq" class="mt-1 font-mono text-lg font-semibold tabular-nums">
					#{liveEvents.lastEvent.seq}
				</div>
			</div>
			<div class="rounded-[var(--radius)] border border-border-strong p-3">
				<div class="text-xs text-muted">Độ trễ tới UI</div>
				<div class="mt-1 font-mono text-lg font-semibold tabular-nums">
					{liveEvents.lastEvent.latencyMs} ms
				</div>
			</div>
			<div class="rounded-[var(--radius)] border border-border-strong p-3">
				<div class="text-xs text-muted">Chủ đề</div>
				<div class="mt-1 font-mono text-lg font-semibold">{liveEvents.lastEvent.topic}</div>
			</div>
		</div>

		<div class="mt-4 overflow-x-auto rounded-[var(--radius)] border border-border-strong">
			<table class="w-full text-left text-sm">
				<thead class="border-b border-border-strong bg-background text-xs text-muted">
					<tr class="h-10">
						<th class="px-3 font-medium">Lượt</th>
						<th class="px-3 font-medium">Server epoch (ms)</th>
						<th class="px-3 text-right font-medium">Độ trễ (ms)</th>
					</tr>
				</thead>
				<tbody>
					{#each liveEvents.history as item (item.seq)}
						<tr class="h-10 border-b border-border-strong last:border-b-0 hover:bg-surface-hover">
							<td class="px-3 font-mono tabular-nums">#{item.seq}</td>
							<td class="px-3 font-mono tabular-nums text-muted">{item.serverSentMs}</td>
							<td class="px-3 text-right font-mono tabular-nums">{item.latencyMs}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</section>
