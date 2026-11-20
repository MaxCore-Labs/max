<script lang="ts">
	import type { Schemas } from '#lib/api/client.js';
	import { t } from '#lib/i18n/vi.js';
	import { Button, Card, Dialog, Field, Input } from '#lib/ui/index.js';
	import ArrowDown from '@lucide/svelte/icons/arrow-down';
	import ArrowUp from '@lucide/svelte/icons/arrow-up';

	export interface EnginePickerValue {
		engines: string[];
		models: Record<string, string>;
		allowUngatedEngine: boolean;
	}

	interface Props {
		availableEngines: Schemas['EngineSettingItem'][];
		selectedEngines: string[];
		models: Record<string, string>;
		sensitive?: boolean;
		browser?: string;
		allowUngatedEngine?: boolean;
		saving?: boolean;
		onsave?: (val: EnginePickerValue) => void | Promise<void>;
		class?: string;
	}

	let {
		availableEngines,
		selectedEngines,
		models,
		sensitive = false,
		browser = 'none',
		allowUngatedEngine = false,
		saving = false,
		onsave,
		class: className = ''
	}: Props = $props();

	let chosen = $derived(selectedEngines.length > 0 ? [...selectedEngines] : ['agy']);
	let modelDraft = $derived<Record<string, string>>({ ...models });
	let confirmOpen = $state(false);

	let enabledList = $derived(availableEngines.filter((e) => e.enabled));
	let isSensitiveRole = $derived(sensitive || browser === 'owner');

	let orderedList = $derived.by(() => {
		const activeNames = enabledList.map((e) => e.name);
		const inOrder = chosen.filter((n) => activeNames.includes(n));
		const rest = activeNames.filter((n) => !inOrder.includes(n));
		return [...inOrder, ...rest];
	});

	function isolationLevel(engineName: string): number {
		if (engineName === 'claude') return 1;
		if (engineName === 'codex') return 2;
		return 3;
	}

	function needsUngatedConfirm(): boolean {
		if (!isSensitiveRole) return false;
		return chosen.some((eng) => isolationLevel(eng) > 1);
	}

	function toggle(name: string): void {
		if (chosen.includes(name)) {
			if (chosen.length <= 1) return;
			chosen = chosen.filter((x) => x !== name);
		} else {
			chosen = [...chosen, name];
		}
	}

	function move(name: string, dir: -1 | 1): void {
		const idx = chosen.indexOf(name);
		const target = idx + dir;
		if (idx < 0 || target < 0 || target >= chosen.length) return;
		const copy = [...chosen];
		const [item] = copy.splice(idx, 1);
		if (item) copy.splice(target, 0, item);
		chosen = copy;
	}

	function handleSaveClick(): void {
		if (chosen.length === 0) return;
		if (needsUngatedConfirm() && !allowUngatedEngine) {
			confirmOpen = true;
			return;
		}
		void emitSave(allowUngatedEngine || needsUngatedConfirm());
	}

	async function emitSave(unlock: boolean): Promise<void> {
		confirmOpen = false;
		const cleanModels: Record<string, string> = {};
		for (const [k, v] of Object.entries(modelDraft)) {
			if (v.trim()) cleanModels[k] = v.trim();
		}
		await onsave?.({
			engines: [...chosen],
			models: cleanModels,
			allowUngatedEngine: unlock
		});
	}
</script>

<Card title={t('agents.engine_picker')} class={className}>
	<div class="flex flex-col gap-3">
		{#each orderedList as name (name)}
			{@const isChecked = chosen.includes(name)}
			{@const pos = chosen.indexOf(name)}
			{@const lockUncheck = isChecked && chosen.length <= 1}
			<div
				data-testid="engine-opt-{name}"
				class="flex flex-wrap items-center justify-between gap-3 rounded-[var(--radius)] border border-border-strong p-3"
			>
				<label class="flex items-center gap-2.5 text-sm font-medium text-foreground">
					<input
						type="checkbox"
						data-testid="engine-check-{name}"
						checked={isChecked}
						disabled={lockUncheck}
						onchange={() => toggle(name)}
						class="h-4 w-4 accent-primary"
					/>
					<span class="font-mono">{name}</span>
					<span class="text-xs text-muted">Mức cô lập {isolationLevel(name)}</span>
				</label>

				<div class="flex items-center gap-2">
					<div class="w-40">
						<Field label="Model">
							<Input
								value={modelDraft[name] ?? ''}
								oninput={(ev) => {
									modelDraft = {
										...modelDraft,
										[name]: (ev.currentTarget as HTMLInputElement).value
									};
								}}
								placeholder="default"
								class="font-mono"
							/>
						</Field>
					</div>
					{#if isChecked}
						<Button
							variant="outline"
							icon={ArrowUp}
							iconOnly
							ariaLabel="Tăng ưu tiên {name}"
							disabled={pos <= 0}
							onclick={() => move(name, -1)}
						/>
						<Button
							variant="outline"
							icon={ArrowDown}
							iconOnly
							ariaLabel="Giảm ưu tiên {name}"
							disabled={pos < 0 || pos >= chosen.length - 1}
							onclick={() => move(name, 1)}
						/>
					{/if}
				</div>
			</div>
		{/each}

		<div class="flex justify-end">
			<Button variant="primary" loading={saving} onclick={handleSaveClick}>
				{t('common.save')}
			</Button>
		</div>
	</div>
</Card>

<Dialog bind:open={confirmOpen} title={t('agents.ungated_title')} busy={saving}>
	<p>{t('agents.ungated_body')}</p>
	{#snippet actions()}
		<Button variant="outline" onclick={() => (confirmOpen = false)}>{t('common.cancel')}</Button>
		<Button variant="danger" loading={saving} onclick={() => void emitSave(true)}>
			{t('agents.ungated_confirm')}
		</Button>
	{/snippet}
</Dialog>
