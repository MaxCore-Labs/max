<script lang="ts">
	import { api, type Schemas } from '#lib/api/client.js';
	import { t } from '#lib/i18n/vi.js';
	import { Button, Card, StatusBadge, toast } from '#lib/ui/index.js';
	import Check from '@lucide/svelte/icons/check';

	interface Props {
		approval: Schemas['Approval'];
		ondecided?: (updated: Schemas['Approval']) => void;
		class?: string;
	}

	let { approval, ondecided, class: className = '' }: Props = $props();
	let overriddenStatus = $state<string | null>(null);
	let submitting = $state(false);

	let effectiveStatus = $derived(overriddenStatus ?? approval.status);
	let isPending = $derived(effectiveStatus === 'pending');

	let initials = $derived(
		approval.agentId
			.split(/[-_\s]+/)
			.slice(0, 2)
			.map((p) => p[0]?.toUpperCase() ?? '')
			.join('') || 'AG'
	);

	let previewPairs = $derived.by(() => {
		const lines = approval.preview.split(/\r?\n/).filter((l) => l.trim().length > 0);
		const pairs: { key: string; value: string }[] = [];
		for (const line of lines) {
			const colon = line.indexOf(':');
			if (colon > 0 && colon <= 24) {
				pairs.push({ key: line.slice(0, colon).trim(), value: line.slice(colon + 1).trim() });
			} else {
				pairs.push({ key: 'Nội dung', value: line.trim() });
			}
		}
		return pairs.length > 0 ? pairs : [{ key: 'Chi tiết', value: approval.preview }];
	});

	let expiryLabel = $derived.by(() => {
		const hoursLeft = Math.max(0, Math.round((approval.expiresAt - Date.now()) / 3_600_000));
		return isPending ? `Hết hạn sau ${hoursLeft} giờ` : '';
	});

	async function decide(allow: boolean): Promise<void> {
		if (submitting || !isPending) return;
		submitting = true;
		try {
			const updated = await api.decideApproval(approval.id, { allow });
			overriddenStatus = updated.status;
			ondecided?.(updated);
		} catch (err) {
			toast.show({
				kind: 'error',
				messageKey: err instanceof Error ? err.message : 'Không xử lý được yêu cầu duyệt'
			});
		} finally {
			submitting = false;
		}
	}
</script>

<Card class={className}>
	<div class="flex flex-wrap items-start justify-between gap-3">
		<div class="flex min-w-0 flex-1 items-center gap-3">
			<span
				class="inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-[var(--radius)] bg-background text-sm font-semibold text-foreground"
			>
				{initials}
			</span>
			<div class="min-w-0">
				<p class="truncate text-sm font-semibold text-foreground">{approval.reason}</p>
				<p class="truncate text-xs text-muted">{approval.agentId}</p>
			</div>
		</div>
		<StatusBadge kind="approval" status={effectiveStatus} />
	</div>

	<dl class="my-4 grid grid-cols-1 gap-x-3 gap-y-2 text-sm sm:grid-cols-[96px_1fr]">
		{#each previewPairs as row, idx (`${row.key}-${idx}`)}
			<dt class="text-muted">{row.key}</dt>
			<dd class="min-w-0 rounded-[var(--radius)] bg-background px-3 py-2 break-words text-foreground">
				{row.value}
			</dd>
		{/each}
	</dl>

	<div class="flex flex-wrap items-center justify-between gap-3">
		<span class="text-xs text-muted">{expiryLabel}</span>
		{#if isPending}
			<div class="flex flex-wrap gap-2">
				<Button variant="outline" disabled={submitting} onclick={() => decide(false)}>
					{t('common.deny')}
				</Button>
				<Button
					variant="primary"
					icon={Check}
					loading={submitting}
					onclick={() => decide(true)}
				>
					{t('common.allow')}
				</Button>
			</div>
		{/if}
	</div>
</Card>
