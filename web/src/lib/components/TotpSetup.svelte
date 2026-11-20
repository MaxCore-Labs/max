<script lang="ts">
	import { api } from '#lib/api/client.js';
	import { Button, Card, Field, Input, toast } from '#lib/ui/index.js';

	interface Props {
		enabled: boolean;
		onenabled: () => void;
	}

	let { enabled, onenabled }: Props = $props();
	const uid = $props.id();

	let secret = $state('');
	let otpauthUrl = $state('');
	let code = $state('');
	let busy = $state(false);
	let error = $state('');

	async function startSetup(): Promise<void> {
		busy = true;
		error = '';
		try {
			const res = await api.setupTotp();
			secret = res.secret;
			otpauthUrl = res.otpauthUrl;
		} catch (err) {
			error = err instanceof Error ? err.message : 'Không khởi tạo được TOTP';
		} finally {
			busy = false;
		}
	}

	async function confirmSetup(e: SubmitEvent): Promise<void> {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			await api.confirmTotp(code.trim());
			secret = '';
			otpauthUrl = '';
			code = '';
			toast.show({ kind: 'info', messageKey: 'Đã bật xác thực 2 bước (TOTP)' });
			onenabled();
		} catch (err) {
			error = err instanceof Error ? err.message : 'Mã TOTP không hợp lệ';
		} finally {
			busy = false;
		}
	}
</script>

<Card title="Xác thực 2 bước (TOTP)">
	<div class="flex flex-col gap-3 text-sm">
		{#if enabled}
			<p class="text-success">Đã bật xác thực 2 bước cho tài khoản chủ nhân.</p>
		{:else if !secret}
			<p class="text-muted">Bảo vệ tài khoản chủ nhân bằng mã 6 chữ số RFC 6238.</p>
			<div>
				<Button variant="outline" loading={busy} onclick={() => void startSetup()}>
					Thiết lập TOTP
				</Button>
			</div>
		{:else}
			<div class="rounded-lg border border-border-strong bg-background p-3 font-mono text-xs">
				<div>Khóa Base32: <b class="text-foreground">{secret}</b></div>
				<div class="mt-1 break-all text-muted">{otpauthUrl}</div>
			</div>
			<form class="flex flex-wrap items-end gap-3" onsubmit={confirmSetup}>
				<Field label="Mã 6 chữ số từ ứng dụng xác thực" for="{uid}-code">
					<Input id="{uid}-code" bind:value={code} placeholder="123456" />
				</Field>
				<Button type="submit" variant="primary" loading={busy}>Xác nhận bật</Button>
			</form>
		{/if}
		{#if error}
			<p class="text-xs text-error-text">{error}</p>
		{/if}
	</div>
</Card>
