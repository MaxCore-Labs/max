<script lang="ts">
	import { Button, Card } from '#lib/ui/index.js';

	interface Props {
		active: boolean;
		url: string;
		origin?: string;
		onstop: () => void;
	}

	let { active, url, origin = '', onstop }: Props = $props();
</script>

{#if active && url}
	<Card title="Tiếp quản trình duyệt (Chủ nhân)">
		<div class="flex flex-col gap-3 text-sm">
			<p class="text-muted">
				Agent đang tạm dừng để bạn trực tiếp đăng nhập hoặc vượt bước xác thực trên
				{#if origin}
					<b class="font-mono text-xs text-foreground">{origin}</b>
				{:else}
					trình duyệt
				{/if}
				qua cổng nội bộ loopback (tự đóng sau 15 phút không kết nối).
			</p>
			<div class="flex flex-wrap items-center gap-3">
				<a
					href={url}
					target="_blank"
					rel="noopener noreferrer"
					class="inline-flex h-10 items-center justify-center rounded-xl bg-accent px-4 text-sm font-medium text-accent-fg no-underline"
				>
					Mở phiên tiếp quản
				</a>
				<Button variant="outline" onclick={onstop}>Kết thúc tiếp quản</Button>
			</div>
			<div class="font-mono text-xs text-muted">{url}</div>
		</div>
	</Card>
{/if}
