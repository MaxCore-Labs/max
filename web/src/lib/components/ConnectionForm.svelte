<script lang="ts">
	import type { Schemas } from '#lib/api/client.js';
	import { Button, Field, Input, Select, Textarea } from '#lib/ui/index.js';

	interface Props {
		existing?: Schemas['ConnectionItem'] | null;
		loading?: boolean;
		onsave: (name: string, req: Schemas['UpsertConnectionRequest']) => void;
	}

	let { existing = null, loading = false, onsave }: Props = $props();
	const uid = $props.id();

	let name = $derived(existing?.name ?? '');
	let kind = $derived(existing?.kind ?? 'http');
	let enabled = $derived(existing?.enabled ?? true);
	let toolsSpec = $derived(
		existing?.tools
			.map((t) => `${t.name}:${t.write ? 'write' : 'read'}${t.targetField ? ':' + t.targetField : ''}`)
			.join('\n') ?? ''
	);
	// Secrets are strictly write-only: never prefilled from existing connection metadata.
	let secretJson = $state('');
	let error = $state('');

	function handleSubmit(e: SubmitEvent): void {
		e.preventDefault();
		error = '';
		const cleanName = name.trim();
		if (!cleanName) {
			error = 'Vui lòng nhập tên kết nối.';
			return;
		}
		let configObj: Record<string, unknown> | undefined;
		if (secretJson.trim()) {
			try {
				configObj = JSON.parse(secretJson.trim()) as Record<string, unknown>;
			} catch {
				error = 'Cấu hình bí mật phải là JSON hợp lệ.';
				return;
			}
		}
		const tools: Schemas['ConnToolItem'][] = toolsSpec
			.split('\n')
			.map((l) => l.trim())
			.filter(Boolean)
			.map((line) => {
				const [tName = '', mode = 'read', target = ''] = line.split(':');
				return {
					name: tName.trim(),
					write: mode.trim() === 'write',
					targetField: target.trim() || undefined
				};
			});

		onsave(cleanName, {
			kind,
			enabled,
			config: configObj,
			tools
		});
		secretJson = '';
	}
</script>

<form class="flex flex-col gap-3" onsubmit={handleSubmit}>
	<div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
		<Field label="Tên kết nối" for="{uid}-name">
			<Input id="{uid}-name" bind:value={name} disabled={Boolean(existing)} placeholder="crm" />
		</Field>
		<Field label="Loại giao thức" for="{uid}-kind">
			<Select
				id="{uid}-kind"
				bind:value={kind}
				options={[
					{ value: 'http', label: 'HTTP JSON-RPC' },
					{ value: 'stdio', label: 'Stdio MCP' }
				]}
			/>
		</Field>
	</div>

	<Field label="Cấu hình bí mật (JSON chỉ ghi)" for="{uid}-secret">
		{#if existing?.configured}
			<p class="mb-1 text-xs text-success">Đã cấu hình — để trống nếu giữ nguyên khoá hiện tại.</p>
		{/if}
		<Textarea
			id="{uid}-secret"
			rows={3}
			bind:value={secretJson}
			placeholder={`{"url":"https://mcp.example.com","bearer":"..."}`}
			class="font-mono text-xs"
		/>
	</Field>

	<Field label="Danh sách tool (mỗi dòng: tên:read|write[:targetField])" for="{uid}-tools">
		<Textarea
			id="{uid}-tools"
			rows={3}
			bind:value={toolsSpec}
			placeholder="send_email:write:to"
			class="font-mono text-xs"
		/>
	</Field>

	<label class="flex items-center gap-2 text-xs text-foreground">
		<input type="checkbox" bind:checked={enabled} /> Đang bật kết nối này
	</label>

	{#if error}
		<p class="text-xs text-error-text">{error}</p>
	{/if}

	<div class="flex justify-end">
		<Button type="submit" variant="primary" {loading}>Lưu kết nối</Button>
	</div>
</form>
