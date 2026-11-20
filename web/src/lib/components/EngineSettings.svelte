<script lang="ts">
	import { api, type Schemas } from '#lib/api/client.js';
	import { t } from '#lib/i18n/vi.js';
	import { Button, Card, Field, Input, Select, StatusBadge, toast } from '#lib/ui/index.js';

	interface Props {
		defaultEngine: string;
		settings: Schemas['EngineSettingItem'][];
		statuses: Schemas['EngineStatusItem'][];
		onsaved?: (res: Schemas['SettingsResponse']) => void;
		class?: string;
	}

	let { defaultEngine, settings, statuses, onsaved, class: className = '' }: Props = $props();

	let pickedDefault = $state('');
	let draft = $state< { name: string; enabled: boolean; slots: string; defaultModel: string }[] >([]);
	let slotErrors = $state<Record<string, string>>({});
	let globalError = $state('');
	let saving = $state(false);

	$effect.pre(() => {
		pickedDefault = defaultEngine;
		draft = settings.map((s) => ({
			name: s.name,
			enabled: s.enabled,
			slots: String(s.slots),
			defaultModel: s.defaultModel
		}));
	});

	let enabledCount = $derived(draft.filter((d) => d.enabled).length);

	function statusOf(name: string): Schemas['EngineStatusItem'] | undefined {
		return statuses.find((s) => s.name === name);
	}

	function loginCommand(name: string): string {
		return name === 'agy' ? 'agy' : `${name} login`;
	}

	function toggleEngine(idx: number): void {
		const row = draft[idx];
		if (!row) return;
		const st = statusOf(row.name);
		if (!row.enabled && st && !st.loggedIn) return;
		if (row.enabled && enabledCount <= 1) {
			globalError = t('system.last_engine_err');
			return;
		}
		globalError = '';
		row.enabled = !row.enabled;
	}

	async function save(): Promise<void> {
		const nextErrors: Record<string, string> = {};
		const payload: Schemas['EngineSettingItem'][] = [];

		for (const row of draft) {
			const n = Number(row.slots);
			if (!Number.isInteger(n) || n < 1 || n > 8) {
				nextErrors[row.name] = t('system.slots_range_err');
			} else {
				payload.push({
					name: row.name,
					enabled: row.enabled,
					slots: n,
					defaultModel: row.defaultModel || 'default'
				});
			}
		}

		slotErrors = nextErrors;
		if (Object.keys(nextErrors).length > 0) return;
		if (!payload.some((p) => p.enabled)) {
			globalError = t('system.last_engine_err');
			return;
		}

		saving = true;
		globalError = '';
		try {
			const res = await api.updateSettings({
				defaultEngine: pickedDefault,
				engines: payload
			});
			toast.show({ kind: 'info', messageKey: 'Đã lưu cài đặt engine' });
			onsaved?.(res);
		} catch (err) {
			globalError = err instanceof Error ? err.message : 'Không lưu được cài đặt';
		} finally {
			saving = false;
		}
	}
</script>

<Card title={t('system.engines')} class={className}>
	<div class="flex flex-col gap-4">
		<div class="max-w-xs">
			<Field label="Engine mặc định">
				<Select
					bind:value={pickedDefault}
					options={draft.filter((d) => d.enabled).map((d) => ({ value: d.name, label: d.name }))}
				/>
			</Field>
		</div>

		{#each draft as row, idx (row.name)}
			{@const st = statusOf(row.name)}
			{@const loggedIn = st ? st.loggedIn : true}
			{@const lockToggle = (!row.enabled && !loggedIn) || (row.enabled && enabledCount <= 1)}
			<div class="flex flex-col gap-3 rounded-[var(--radius)] border border-border-strong p-4">
				<div class="flex flex-wrap items-center justify-between gap-3">
					<div class="flex items-center gap-2.5">
						<span class="font-mono text-sm font-semibold">{row.name}</span>
						<StatusBadge
							kind="engine"
							status={!loggedIn ? 'logged_out' : row.enabled ? 'ready' : 'disabled'}
						/>
					</div>
					<button
						type="button"
						data-testid="engine-toggle-{row.name}"
						disabled={lockToggle}
						onclick={() => toggleEngine(idx)}
						class="inline-flex h-8 cursor-pointer items-center rounded-[var(--radius-sm)] border border-border-strong bg-surface px-3 text-xs font-medium text-foreground hover:bg-button-hover disabled:cursor-not-allowed disabled:opacity-50"
					>
						{row.enabled ? t('system.enabled') : t('system.disabled')}
					</button>
				</div>

				{#if !loggedIn}
					<p class="font-mono text-xs text-error-text">
						{t('system.login_hint', { cmd: loginCommand(row.name) })}
					</p>
				{/if}

				<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
					<Field
						label="{t('system.slots_label')} · {row.name}"
						error={slotErrors[row.name] ?? ''}
					>
						<Input type="number" min={1} max={8} bind:value={row.slots} />
					</Field>
					<Field label="Model mặc định · {row.name}">
						<Input bind:value={row.defaultModel} class="font-mono" />
					</Field>
				</div>
			</div>
		{/each}

		{#if globalError}
			<p class="text-xs text-error-text">{globalError}</p>
		{/if}

		<div class="flex justify-end">
			<Button variant="primary" loading={saving} onclick={save}>{t('common.save')}</Button>
		</div>
	</div>
</Card>
