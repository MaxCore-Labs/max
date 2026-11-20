<script lang="ts">
	import { ApiError } from '#lib/api/client.js';
	import { t } from '#lib/i18n/vi.js';
	import { Button, Card, Field, Input, Select, Textarea } from '#lib/ui/index.js';

	export interface RoutineSubmitPayload {
		slug: string;
		agent: string;
		schedule: string;
		origin: 'trusted' | 'inbound';
		catchUp: 'run_once' | 'skip';
		enabled: boolean;
		body: string;
	}

	interface Props {
		agents: { slug: string; name: string }[];
		initialSlug?: string;
		initialAgent?: string;
		initialSchedule?: string;
		initialOrigin?: string;
		initialBody?: string;
		saving?: boolean;
		onsubmit?: (payload: RoutineSubmitPayload) => void | Promise<void>;
		class?: string;
	}

	const CRON_FIELD_RE = /^(\*|\*\/\d+|\d+(-\d+)?(,\d+(-\d+)?)*(\/\d+)?)$/;

	let {
		agents,
		initialSlug = '',
		initialAgent = '',
		initialSchedule = '0 8 * * 1-5',
		initialOrigin = 'trusted',
		initialBody = '',
		saving = false,
		onsubmit,
		class: className = ''
	}: Props = $props();

	let slug = $derived(initialSlug);
	let agent = $derived(initialAgent || agents[0]?.slug || 'chief-of-staff');
	let schedule = $derived(initialSchedule);
	let readsExternal = $derived(initialOrigin === 'inbound');
	let catchUp = $state<'run_once' | 'skip'>('run_once');
	let body = $derived(initialBody);

	let slugError = $state('');
	let scheduleError = $state('');
	let generalError = $state('');

	function isValidCron5(expr: string): boolean {
		const parts = expr.trim().split(/\s+/);
		if (parts.length !== 5) return false;
		return parts.every((p) => CRON_FIELD_RE.test(p));
	}

	async function handleSubmit(): Promise<void> {
		slugError = '';
		scheduleError = '';
		generalError = '';

		const cleanSlug = slug.trim();
		if (!/^[a-z0-9][a-z0-9_-]{0,63}$/.test(cleanSlug)) {
			slugError = 'Slug không hợp lệ (chỉ chữ thường, số, gạch ngang)';
			return;
		}
		if (!isValidCron5(schedule)) {
			scheduleError = 'Biểu thức cron 5 trường không hợp lệ (ví dụ: 0 8 * * 1-5)';
			return;
		}

		try {
			await onsubmit?.({
				slug: cleanSlug,
				agent,
				schedule: schedule.trim(),
				origin: readsExternal ? 'inbound' : 'trusted',
				catchUp,
				enabled: true,
				body
			});
		} catch (err) {
			if (err instanceof ApiError && err.field === 'schedule') {
				scheduleError = err.message;
			} else if (err instanceof ApiError && err.field === 'slug') {
				slugError = err.message;
			} else {
				generalError = err instanceof Error ? err.message : 'Lưu routine thất bại';
			}
		}
	}
</script>

<Card title={t('routines.new')} class={className}>
	<div class="flex flex-col gap-3">
		<div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
			<Field label="Slug" error={slugError}>
				<Input id="routine-slug" bind:value={slug} placeholder="morning-brief" class="font-mono" />
			</Field>
			<Field label="Agent thực hiện">
				<Select
					bind:value={agent}
					options={agents.map((a) => ({ value: a.slug, label: `${a.name} (${a.slug})` }))}
				/>
			</Field>
		</div>

		<div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
			<Field label="Lịch Cron (5 trường)" error={scheduleError}>
				<Input id="routine-schedule" bind:value={schedule} class="font-mono" />
			</Field>
			<Field label="Chạy bù sau downtime">
				<Select
					bind:value={catchUp}
					options={[
						{ value: 'run_once', label: 'Chạy bù 1 lần (run_once)' },
						{ value: 'skip', label: 'Bỏ qua (skip)' }
					]}
				/>
			</Field>
		</div>

		<label class="flex items-center gap-2 text-sm text-foreground">
			<input
				type="checkbox"
				data-testid="routine-inbound-check"
				bind:checked={readsExternal}
				class="h-4 w-4 accent-primary"
			/>
			<span>{t('routines.external_label')}</span>
		</label>

		<Field label="Nội dung giao việc">
			<Textarea id="routine-body" rows={4} bind:value={body} />
		</Field>

		{#if generalError}
			<p class="text-xs text-error-text">{generalError}</p>
		{/if}

		<div class="flex justify-end">
			<Button variant="primary" loading={saving} onclick={() => void handleSubmit()}>
				{t('common.save')}
			</Button>
		</div>
	</div>
</Card>
