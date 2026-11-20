<script lang="ts">
	import type { Schemas } from '#lib/api/client.js';
	import { Card, EmptyState } from '#lib/ui/index.js';

	interface Props {
		engines: Schemas['EngineStatusItem'][];
		queue: Schemas['QueueSummary'];
	}

	let { engines, queue }: Props = $props();

	function priorityLabel(p: number): string {
		if (p >= 100) return 'Chat chủ nhân (100)';
		if (p >= 50) return 'Công việc (50)';
		if (p >= 30) return 'Lịch định kỳ (30)';
		return `Bảo trì / Heartbeat (${p})`;
	}

	function reasonLabel(r: string): string {
		switch (r) {
			case 'slots_full':
				return 'Hết chỗ';
			case 'quota_limited':
				return 'Hết hạn mức';
			case 'cooldown':
				return 'Cooldown';
			default:
				return 'Đang nhận';
		}
	}
</script>

<Card title="Bảng năng lực & Độ trễ hàng đợi">
	<div class="flex flex-col gap-4 text-sm">
		<div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
			{#each engines as eng (eng.name)}
				<div class="rounded-xl border border-border-strong bg-background p-3">
					<div class="flex items-center justify-between">
						<span class="font-mono text-xs font-semibold text-foreground">{eng.name}</span>
						<span class="font-mono text-xs text-muted tabular-nums">
							{eng.used}/{eng.slots} chỗ
						</span>
					</div>
					<div class="mt-1 text-xs text-muted">
						{#if !eng.enabled}
							Đã tắt
						{:else if eng.limitedUntil}
							Tạm giữ hạn mức
						{:else}
							Sẵn sàng nhận việc
						{/if}
					</div>
				</div>
			{/each}
		</div>

		<div class="flex flex-wrap items-center justify-between gap-2 text-xs text-muted tabular-nums">
			<span>Tổng hàng đợi: {queue.total}</span>
			<span class="font-medium text-foreground">
				Việc trễ vì hạn mức: {queue.quotaDelayed ?? 0}
			</span>
		</div>

		{#if !queue.backlog || queue.backlog.length === 0}
			<EmptyState message="Không có việc nào đang nghẽn trong hàng đợi." />
		{:else}
			<div class="overflow-x-auto">
				<table class="w-full border-collapse text-left text-sm">
					<thead>
						<tr class="border-b border-border-strong text-xs text-muted">
							<th class="h-10 pr-3 font-medium">Lớp ưu tiên</th>
							<th class="h-10 px-3 text-right font-medium">Đang chờ</th>
							<th class="h-10 px-3 text-right font-medium">Chờ lâu nhất</th>
							<th class="h-10 pl-3 font-medium">Lý do chính</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-border">
						{#each queue.backlog as item (item.priority)}
							<tr class="h-10">
								<td class="pr-3 font-medium text-foreground">{priorityLabel(item.priority)}</td>
								<td class="px-3 text-right font-mono text-xs tabular-nums">{item.count}</td>
								<td class="px-3 text-right font-mono text-xs tabular-nums">
									{item.oldestAgeSec} giây
								</td>
								<td class="pl-3 text-xs text-muted">{reasonLabel(item.reason)}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	</div>
</Card>
