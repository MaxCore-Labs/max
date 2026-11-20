<script lang="ts">
	import { api, type Schemas } from '#lib/api/client.js';
	import { t } from '#lib/i18n/vi.js';
	import { Button, Dialog, Field, Input, Select, Textarea, toast } from '#lib/ui/index.js';

	interface Props {
		open?: boolean;
		templates: Schemas['RoleTemplate'][];
		teams: Schemas['Team'][];
		oncreated?: (res: Schemas['CreateAgentsBulkResponse']) => void;
	}

	interface PreviewRow {
		slug: string;
		params: Record<string, string>;
		missing: string[];
	}

	let { open = $bindable(false), templates, teams, oncreated }: Props = $props();

	let templateSlug = $derived(templates[0]?.slug ?? '');
	let teamSlug = $derived(teams[0]?.slug ?? '');
	let slugPrefix = $state('');
	let rawText = $state('');
	let saving = $state(false);
	let errorMsg = $state('');

	let activeTemplate = $derived(templates.find((tpl) => tpl.slug === templateSlug));
	let effectivePrefix = $derived((slugPrefix.trim() || templateSlug || 'agent').toLowerCase());

	let previewRows = $derived.by<PreviewRow[]>(() => {
		const lines = rawText
			.split(/\r?\n/)
			.map((l) => l.trim())
			.filter(Boolean);
		const reqParams = activeTemplate?.requiredParams ?? [];

		return lines.map((line, idx) => {
			const parts = line.split(/[,\t]/).map((p) => p.trim());
			const params: Record<string, string> = {};
			if (reqParams.length === 0) {
				params.name = parts[0] ?? '';
			} else {
				reqParams.forEach((pName, pIdx) => {
					const cell = parts[pIdx] ?? '';
					if (cell) params[pName] = cell;
				});
			}
			const missing = reqParams.filter((p) => !params[p]);
			return {
				slug: `${effectivePrefix}-${idx + 1}`,
				params,
				missing
			};
		});
	});

	let hasInvalidRows = $derived(previewRows.some((r) => r.missing.length > 0));

	async function submit(): Promise<void> {
		if (previewRows.length === 0 || hasInvalidRows) return;
		saving = true;
		errorMsg = '';
		try {
			const res = await api.createAgentsBulk({
				template: templateSlug,
				team: teamSlug,
				slugPrefix: effectivePrefix,
				rows: previewRows.map((r) => r.params)
			});
			toast.show({ kind: 'info', messageKey: `Đã tạo ${res.created.length} agent` });
			rawText = '';
			open = false;
			oncreated?.(res);
		} catch (err) {
			errorMsg = err instanceof Error ? err.message : 'Tạo hàng loạt thất bại';
		} finally {
			saving = false;
		}
	}
</script>

<Dialog bind:open title={t('agents.bulk_create')} busy={saving}>
	<div class="flex flex-col gap-3 text-foreground">
		<div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
			<Field label={t('agents.col.template')}>
				<Select
					bind:value={templateSlug}
					options={templates.map((tpl) => ({ value: tpl.slug, label: tpl.name }))}
				/>
			</Field>
			<Field label="Team">
				<Select
					bind:value={teamSlug}
					options={teams.map((tm) => ({ value: tm.slug, label: tm.name }))}
				/>
			</Field>
		</div>

		<Field label="Tiền tố slug">
			<Input bind:value={slugPrefix} placeholder={templateSlug || 'agent'} class="font-mono" />
		</Field>

		<Field
			label={t('agents.bulk_paste_label')}
			hint={activeTemplate?.requiredParams.length
				? `Thứ tự cột: ${activeTemplate.requiredParams.join(', ')}`
				: undefined}
		>
			<Textarea
				id="bulk-paste-input"
				rows={4}
				bind:value={rawText}
				placeholder="Mỗi dòng một thực thể…"
			/>
		</Field>

		{#if previewRows.length > 0}
			<div class="rounded-[var(--radius)] border border-border-strong p-3">
				<div class="mb-2 text-xs font-semibold text-muted">
					{t('agents.bulk_preview', { count: previewRows.length })}
				</div>
				<ul class="max-h-36 space-y-1 overflow-y-auto text-xs">
					{#each previewRows as row, idx (row.slug)}
						<li
							data-testid="bulk-preview-row-{idx}"
							class="flex items-center justify-between gap-2 py-0.5"
						>
							<span class="font-mono text-foreground">{row.slug}</span>
							{#if row.missing.length > 0}
								<span class="text-error-text">
									{t('agents.bulk_missing', { param: row.missing.join(', ') })}
								</span>
							{:else}
								<span class="text-muted">Hợp lệ</span>
							{/if}
						</li>
					{/each}
				</ul>
			</div>
		{/if}

		{#if errorMsg}
			<p class="text-xs text-error-text">{errorMsg}</p>
		{/if}
	</div>

	{#snippet actions()}
		<Button variant="outline" onclick={() => (open = false)}>{t('common.cancel')}</Button>
		<Button
			variant="primary"
			loading={saving}
			disabled={previewRows.length === 0 || hasInvalidRows}
			onclick={() => void submit()}
		>
			{t('agents.bulk_create')}
		</Button>
	{/snippet}
</Dialog>
