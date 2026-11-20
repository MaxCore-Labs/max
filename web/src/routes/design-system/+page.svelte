<script lang="ts">
	import {
		Button,
		Card,
		DataTable,
		Dialog,
		EmptyState,
		Field,
		Input,
		ListRow,
		Menu,
		Select,
		Skeleton,
		StatusBadge,
		Textarea,
		Tree,
		toast
	} from '#lib/ui/index.js';
	import Check from '@lucide/svelte/icons/check';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Plus from '@lucide/svelte/icons/plus';
	import SlidersHorizontal from '@lucide/svelte/icons/sliders-horizontal';
	import Trash2 from '@lucide/svelte/icons/trash-2';

	let agentName = $state('Trợ lý nghiên cứu');
	let badSlots = $state('12');
	let lockedId = $state('researcher-01');
	let notesVal = $state('Ưu tiên nguồn tiếng Việt. Mỗi kết luận kèm đường dẫn nguồn.');
	let enginePick = $state('agy');
	let dialogOpen = $state(false);
	let treeExpanded = $state(['team-mkt']);

	const swatches = [
		{ name: 'background', cls: 'bg-background', desc: 'nền trang' },
		{ name: 'surface', cls: 'bg-surface', desc: 'nền card' },
		{ name: 'foreground', cls: 'bg-foreground', desc: 'chữ chính' },
		{ name: 'muted', cls: 'bg-muted', desc: 'chữ phụ' },
		{ name: 'primary', cls: 'bg-primary', desc: 'màu nhấn' },
		{ name: 'secondary', cls: 'bg-secondary', desc: 'nút phụ, mục đang chọn' },
		{ name: 'border-strong', cls: 'bg-border-strong', desc: 'viền ô nhập, nút' },
		{ name: 'border', cls: 'bg-border', desc: 'viền card' }
	];

	const sampleAgents = [
		{ id: '1', name: 'Chánh văn phòng', status: 'running', kind: 'run' as const, engine: 'agy', tokens: '184.200' },
		{ id: '2', name: 'Trợ lý nghiên cứu thị trường Đông Nam Á', status: 'quota_stopped', kind: 'run' as const, engine: 'agy', tokens: '1.204.880' },
		{ id: '3', name: 'Trợ lý kế toán', status: 'failed', kind: 'run' as const, engine: 'agy', tokens: '0' },
		{ id: '4', name: 'Người viết', status: 'paused', kind: 'agent' as const, engine: 'agy', tokens: '32.015' }
	];

	const treeNodes = [
		{
			id: 'team-mkt',
			label: 'Marketing',
			children: [
				{ id: 'ag-res', label: 'Trợ lý nghiên cứu' },
				{ id: 'ag-wrt', label: 'Người viết' }
			]
		},
		{ id: 'team-ops', label: 'Vận hành', children: [{ id: 'ag-acc', label: 'Trợ lý kế toán' }] }
	];
</script>

<main class="mx-auto flex max-w-6xl flex-col gap-10 p-4 sm:p-6">
	<header>
		<h1 class="text-xl font-semibold">Max Design System</h1>
		<p class="mt-1 text-sm text-muted">
			Phẳng, trung tính, nền sáng. Màu chỉ dùng để báo trạng thái.
		</p>
	</header>

	<section class="flex flex-col gap-3">
		<h2 class="text-lg font-semibold">Màu &amp; Chữ</h2>
		<div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
			{#each swatches as sw (sw.name)}
				<div class="overflow-hidden rounded-[var(--radius)] border border-border-strong bg-surface">
					<div class="h-14 border-b border-border-strong {sw.cls}"></div>
					<div class="px-3 py-2">
						<div class="text-xs font-medium">{sw.name}</div>
						<div class="font-mono text-xs text-muted">{sw.desc}</div>
					</div>
				</div>
			{/each}
		</div>
	</section>

	<section class="flex flex-col gap-3">
		<h2 class="text-lg font-semibold">Nút</h2>
		<Card>
			<div class="flex flex-wrap items-start gap-4">
				<Button icon={Plus}>Thêm agent</Button>
				<Button variant="primary" icon={Check}>Cho phép</Button>
				<Button variant="secondary">Huỷ</Button>
				<Button variant="ghost">Xem tất cả</Button>
				<Button variant="danger" icon={Trash2}>Xoá agent</Button>
				<Button icon={SlidersHorizontal} iconOnly ariaLabel="Lọc danh sách" />
				<Button variant="ghost" icon={Ellipsis} dense ariaLabel="Thao tác khác" />
				<Button variant="primary" disabled>Khoá</Button>
				<Button variant="primary" loading>Đang xử lý</Button>
			</div>
		</Card>
	</section>

	<section class="flex flex-col gap-3">
		<h2 class="text-lg font-semibold">Badge trạng thái</h2>
		<Card>
			<div class="flex flex-col gap-4">
				<div class="flex flex-wrap gap-2">
					{#each ['running', 'parked', 'quota_stopped', 'interrupted', 'cancelled', 'failed', 'timed_out', 'turn_limit', 'succeeded'] as st (st)}
						<StatusBadge kind="run" status={st} />
					{/each}
				</div>
				<div class="flex flex-wrap gap-2">
					{#each ['todo', 'in_progress', 'in_review', 'waiting_owner', 'cancelled', 'done'] as st (st)}
						<StatusBadge kind="task" status={st} />
					{/each}
					<StatusBadge kind="task" status="blocked" reason="dependency" />
					<StatusBadge kind="task" status="blocked" reason="stalled" />
					<StatusBadge kind="task" status="blocked" reason="error" />
				</div>
				<div class="flex flex-wrap gap-2">
					{#each ['pending', 'owner_question', 'ambiguous', 'allowed', 'denied', 'expired'] as st (st)}
						<StatusBadge kind="approval" status={st} />
					{/each}
				</div>
				<div class="flex flex-wrap gap-2">
					{#each ['active', 'paused', 'quota_paused'] as st (st)}
						<StatusBadge kind="agent" status={st} />
					{/each}
					{#each ['ready', 'limited', 'logged_out', 'disabled'] as st (st)}
						<StatusBadge kind="engine" status={st} />
					{/each}
				</div>
			</div>
		</Card>
	</section>

	<section class="flex flex-col gap-3">
		<h2 class="text-lg font-semibold">Ô nhập</h2>
		<Card>
			<div class="grid grid-cols-1 gap-5 sm:grid-cols-2 lg:grid-cols-4">
				<Field label="Tên agent" hint="Hiện trong danh sách và trong chat.">
					<Input bind:value={agentName} />
				</Field>
				<Field label="Số chỗ của agy" error="Số chỗ phải từ 1 đến 8.">
					<Input bind:value={badSlots} />
				</Field>
				<Field label="Mã agent" hint="Không đổi được sau khi tạo.">
					<Input bind:value={lockedId} disabled class="font-mono" />
				</Field>
				<Field label="Engine mặc định">
					<Select
						bind:value={enginePick}
						options={[
							{ value: 'agy', label: 'agy' }
						]}
					/>
				</Field>
			</div>
			<div class="mt-4 max-w-xl">
				<Field label="Chỉ dẫn riêng">
					<Textarea bind:value={notesVal} rows={3} />
				</Field>
			</div>
		</Card>
	</section>

	<section class="flex flex-col gap-3">
		<h2 class="text-lg font-semibold">Card, Dòng danh sách, Bảng dày &amp; Cây</h2>
		<div class="grid grid-cols-1 gap-3 md:grid-cols-2">
			<Card title="Danh sách thưa">
				{#snippet action()}
					<Button icon={Plus} onclick={() => toast.show({ kind: 'info', messageKey: 'Đã thêm lịch' })}>
						Thêm lịch
					</Button>
				{/snippet}
				<div class="flex flex-col gap-0.5">
					<ListRow title="Trợ lý nghiên cứu" meta="Đã đọc 12 nguồn · 5 phút trước" initials="NC">
						{#snippet trailing()}<StatusBadge kind="run" status="succeeded" />{/snippet}
					</ListRow>
					<ListRow title="Trợ lý bán hàng muốn gửi email" meta="12 phút trước" initials="BH">
						{#snippet trailing()}<StatusBadge kind="approval" status="pending" />{/snippet}
					</ListRow>
				</div>
			</Card>
			<Card title="Cây tổ chức">
				<Tree nodes={treeNodes} bind:expanded={treeExpanded} />
			</Card>
		</div>

		{#snippet statusCell(row: (typeof sampleAgents)[number])}
			<StatusBadge kind={row.kind} status={row.status} />
		{/snippet}
		{#snippet actionCell(row: (typeof sampleAgents)[number])}
			<Menu
				ariaLabel="Thao tác cho {row.name}"
				items={[
					{ labelKey: 'Xem chi tiết', onselect: () => toast.show({ kind: 'info', messageKey: row.name }) },
					{ labelKey: 'Xoá agent', danger: true, onselect: () => (dialogOpen = true) }
				]}
			>
				{#snippet trigger()}<Ellipsis size={16} strokeWidth={2} aria-hidden="true" />{/snippet}
			</Menu>
		{/snippet}
		<Card flush>
			<DataTable
				rows={sampleAgents}
				rowKey={(r) => r.id}
				columns={[
					{ key: 'name', labelKey: 'Agent' },
					{ key: 'status', labelKey: 'Trạng thái', cell: statusCell },
					{ key: 'engine', labelKey: 'Engine' },
					{ key: 'tokens', labelKey: 'Token hôm nay', align: 'right' },
					{ key: 'act', labelKey: '', align: 'right', cell: actionCell }
				]}
			/>
		</Card>
	</section>

	<section class="grid grid-cols-1 gap-3 sm:grid-cols-3">
		<Card title="Rỗng"><EmptyState message="Không có việc nào đang chờ bạn." /></Card>
		<Card title="Rỗng do lọc">
			<EmptyState message="Không có agent nào khớp.">
				{#snippet action()}
					<button type="button" class="cursor-pointer font-medium text-foreground hover:underline">
						Xoá tìm kiếm
					</button>
				{/snippet}
			</EmptyState>
		</Card>
		<Card title="Đang tải"><Skeleton lines={3} /></Card>
	</section>

	<div class="flex gap-2">
		<Button onclick={() => (dialogOpen = true)}>Mở hộp thoại mẫu</Button>
	</div>

	<Dialog bind:open={dialogOpen} title="Xoá agent &quot;Trợ lý kế toán&quot;?">
		2 việc đang giao cho agent này sẽ quay về hàng chờ của Chánh văn phòng. Ghi chú và lịch sử chạy vẫn được giữ.
		{#snippet actions()}
			<Button variant="secondary" onclick={() => (dialogOpen = false)}>Huỷ</Button>
			<Button variant="danger" icon={Trash2} onclick={() => (dialogOpen = false)}>Xoá agent</Button>
		{/snippet}
	</Dialog>
</main>
