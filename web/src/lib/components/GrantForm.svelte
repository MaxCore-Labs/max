<script lang="ts">
	import type { Schemas } from '#lib/api/client.js';
	import { Button, Field, Input } from '#lib/ui/index.js';

	interface Props {
		loading?: boolean;
		onsubmit: (req: Schemas['CreateGrantRequest']) => void;
	}

	let { loading = false, onsubmit }: Props = $props();
	const uid = $props.id();

	let connection = $state('');
	let tool = $state('');
	let equalsField = $state('');
	let equalsValue = $state('');
	let oneOfField = $state('');
	let oneOfValues = $state('');
	let maxNumField = $state('');
	let maxNumValue = $state('');
	let maxPerDayStr = $state('');
	let originOwner = $state(true);
	let originRoutine = $state(true);
	let originAgent = $state(false);
	let originExternal = $state(false);
	let error = $state('');

	function handleSubmit(e: SubmitEvent): void {
		e.preventDefault();
		error = '';
		const conn = connection.trim();
		const tl = tool.trim();
		if (!conn || !tl) {
			error = 'Vui lòng nhập tên kết nối và tool.';
			return;
		}

		const constraints: Schemas['GrantConstraints'] = {};
		let hasParamConstraint = false;

		if (equalsField.trim() && equalsValue.trim()) {
			constraints.equals = { [equalsField.trim()]: equalsValue.trim() };
			hasParamConstraint = true;
		}
		if (oneOfField.trim() && oneOfValues.trim()) {
			const list = oneOfValues
				.split(',')
				.map((s) => s.trim())
				.filter(Boolean);
			if (list.length > 0) {
				constraints.oneOf = { [oneOfField.trim()]: list };
				hasParamConstraint = true;
			}
		}
		const rawMaxNum = String(maxNumValue ?? '').trim();
		if (maxNumField.trim() && rawMaxNum) {
			const num = Number(rawMaxNum);
			if (!Number.isNaN(num)) {
				constraints.maxNumber = { [maxNumField.trim()]: num };
				hasParamConstraint = true;
			}
		}

		let maxPerDay: number | undefined;
		const rawMaxDay = String(maxPerDayStr ?? '').trim();
		if (rawMaxDay) {
			const parsed = Number.parseInt(rawMaxDay, 10);
			if (!Number.isNaN(parsed) && parsed > 0) {
				maxPerDay = parsed;
			}
		}

		if (!hasParamConstraint && maxPerDay === undefined) {
			error = 'Bắt buộc có ít nhất 1 ràng buộc tham số hoặc giới hạn ngày (max_per_day).';
			return;
		}

		const allowedRootOrigins: string[] = [];
		if (originOwner) allowedRootOrigins.push('owner');
		if (originRoutine) allowedRootOrigins.push('routine');
		if (originAgent) allowedRootOrigins.push('agent');
		if (originExternal) allowedRootOrigins.push('external');

		onsubmit({
			connection: conn,
			tool: tl,
			constraints,
			allowedRootOrigins: allowedRootOrigins.length > 0 ? allowedRootOrigins : ['owner', 'routine'],
			maxPerDay: maxPerDay ?? null
		});
	}
</script>

<form class="flex flex-col gap-3" onsubmit={handleSubmit}>
	<div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
		<Field label="Kết nối" for="{uid}-conn">
			<Input id="{uid}-conn" bind:value={connection} placeholder="gmail" />
		</Field>
		<Field label="Tool" for="{uid}-tool">
			<Input id="{uid}-tool" bind:value={tool} placeholder="send_email" />
		</Field>
	</div>

	<div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
		<Field label="Trường khớp bằng" for="{uid}-eq-f">
			<Input id="{uid}-eq-f" bind:value={equalsField} placeholder="to" />
		</Field>
		<Field label="Giá trị bằng" for="{uid}-eq-v">
			<Input id="{uid}-eq-v" bind:value={equalsValue} placeholder="vip@acme.com" />
		</Field>
		<Field label="Số lần tối đa mỗi ngày" for="{uid}-maxday">
			<Input id="{uid}-maxday" type="number" bind:value={maxPerDayStr} placeholder="10" />
		</Field>
	</div>

	<div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
		<Field label="Trường danh sách (OneOf)" for="{uid}-oo-f">
			<Input id="{uid}-oo-f" bind:value={oneOfField} placeholder="to" />
		</Field>
		<Field label="Danh sách giá trị (phân tách phẩy)" for="{uid}-oo-v">
			<Input id="{uid}-oo-v" bind:value={oneOfValues} placeholder="a@x.com, b@x.com" />
		</Field>
	</div>

	<div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
		<Field label="Trường trần số (MaxNumber)" for="{uid}-mn-f">
			<Input id="{uid}-mn-f" bind:value={maxNumField} placeholder="amount" />
		</Field>
		<Field label="Giá trị tối đa" for="{uid}-mn-v">
			<Input id="{uid}-mn-v" type="number" bind:value={maxNumValue} placeholder="100" />
		</Field>
	</div>

	<fieldset class="flex flex-wrap items-center gap-4 text-xs text-foreground">
		<legend class="mb-1 text-xs font-medium text-muted">Nguồn gốc gốc cho phép (root_origin):</legend>
		<label class="flex items-center gap-1.5">
			<input type="checkbox" bind:checked={originOwner} /> owner
		</label>
		<label class="flex items-center gap-1.5">
			<input type="checkbox" bind:checked={originRoutine} /> routine
		</label>
		<label class="flex items-center gap-1.5">
			<input type="checkbox" bind:checked={originAgent} /> agent
		</label>
		<label class="flex items-center gap-1.5">
			<input type="checkbox" bind:checked={originExternal} /> external
		</label>
	</fieldset>

	{#if error}
		<p class="text-xs text-error-text">{error}</p>
	{/if}

	<div class="flex justify-end">
		<Button type="submit" variant="primary" {loading}>Tạo grant</Button>
	</div>
</form>
