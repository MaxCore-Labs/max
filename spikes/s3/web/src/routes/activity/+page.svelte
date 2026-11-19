<script lang="ts">
	import CircleCheck from '@lucide/svelte/icons/circle-check';

	let dialogEl: HTMLDialogElement | undefined = $state();
	let triggerBtnEl: HTMLButtonElement | undefined = $state();
	let popoverEl: HTMLDivElement | undefined = $state();
	let lastAction = $state('Chưa chọn thao tác');

	function openModal(): void {
		dialogEl?.showModal();
	}

	function closeModal(): void {
		dialogEl?.close();
	}

	function handleDialogBackdropClick(ev: MouseEvent): void {
		if (ev.target === dialogEl) {
			dialogEl?.close();
		}
	}

	// Position native [popover] relative to trigger button via getBoundingClientRect()
	// so anchored placement works identically on Chrome, Firefox, and Safari without CSS Anchor Positioning.
	function handlePopoverToggle(ev: ToggleEvent): void {
		if (ev.newState === 'open' && triggerBtnEl && popoverEl) {
			const rect = triggerBtnEl.getBoundingClientRect();
			popoverEl.style.position = 'fixed';
			popoverEl.style.margin = '0';
			popoverEl.style.top = `${Math.round(rect.bottom + 6)}px`;
			popoverEl.style.left = `${Math.max(8, Math.round(rect.left))}px`;
		}
	}

	function pickMenuItem(label: string): void {
		lastAction = label;
		popoverEl?.hidePopover();
	}
</script>

<section class="rounded-[var(--radius-lg)] border border-border bg-surface p-4 sm:p-5">
	<h1 data-testid="activity-heading" class="text-base font-semibold">
		Hoạt động &amp; Kiểm chứng Design Tokens (`S3` Bước 4b)
	</h1>
	<p class="mt-1 text-sm text-muted">
		Kiểm chứng màu token `--primary`, tree-shaking `@lucide/svelte`, phần tử gốc `&lt;dialog&gt;` và
		`popover`.
	</p>

	<div class="mt-5 flex flex-wrap items-center gap-3">
		<button
			type="button"
			data-testid="primary-btn"
			onclick={openModal}
			class="inline-flex h-10 items-center gap-2 rounded-[var(--radius)] bg-primary px-4 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary-hover"
		>
			<CircleCheck size={16} strokeWidth={2} />
			<span>Mở hộp thoại xác nhận</span>
		</button>

		<button
			bind:this={triggerBtnEl}
			type="button"
			data-testid="popover-trigger"
			popovertarget="activity-actions-menu"
			class="inline-flex h-10 items-center gap-2 rounded-[var(--radius)] border border-border-strong bg-surface px-4 text-sm font-medium text-foreground transition-colors hover:bg-button-hover"
		>
			Mở menu tác vụ (Popover)
		</button>

		<span data-testid="last-action" class="text-xs text-muted">{lastAction}</span>
	</div>

	<div
		bind:this={popoverEl}
		id="activity-actions-menu"
		data-testid="demo-popover"
		popover="auto"
		ontoggle={handlePopoverToggle}
		class="min-w-52 rounded-[var(--radius-lg)] border border-border bg-surface-overlay p-1.5 text-foreground shadow-popover"
	>
		<button
			type="button"
			onclick={() => pickMenuItem('Xem chi tiết run')}
			class="flex h-10 w-full items-center rounded-[var(--radius)] px-3 text-left text-sm hover:bg-item-hover"
		>
			Xem chi tiết run
		</button>
		<button
			type="button"
			onclick={() => pickMenuItem('Tạm dừng agent')}
			class="flex h-10 w-full items-center rounded-[var(--radius)] px-3 text-left text-sm hover:bg-item-hover"
		>
			Tạm dừng agent
		</button>
		<button
			type="button"
			onclick={() => pickMenuItem('Thu hồi quyền')}
			class="flex h-10 w-full items-center rounded-[var(--radius)] px-3 text-left text-sm text-foreground hover:bg-danger-bg hover:text-danger"
		>
			Thu hồi quyền
		</button>
	</div>

	<dialog
		bind:this={dialogEl}
		data-testid="demo-dialog"
		onclick={handleDialogBackdropClick}
		class="m-auto w-full max-w-md rounded-[var(--radius-lg)] border border-border bg-surface-overlay p-0 text-foreground shadow-modal backdrop:bg-foreground/20"
	>
		<div class="p-5">
			<h2 class="text-base font-semibold">Xác nhận thao tác</h2>
			<p class="mt-2 text-sm text-muted">
				Hộp thoại dùng phần tử `&lt;dialog&gt;` chuẩn của trình duyệt, tự bẫy tiêu điểm và đóng khi
				bấm phím Esc.
			</p>
			<div class="mt-5 flex justify-end gap-2">
				<button
					type="button"
					onclick={closeModal}
					class="inline-flex h-10 items-center rounded-[var(--radius)] border border-border-strong bg-surface px-4 text-sm font-medium text-foreground hover:bg-button-hover"
				>
					Huỷ
				</button>
				<button
					type="button"
					onclick={() => {
						lastAction = 'Đã xác nhận hộp thoại';
						closeModal();
					}}
					class="inline-flex h-10 items-center rounded-[var(--radius)] bg-primary px-4 text-sm font-medium text-primary-foreground hover:bg-primary-hover"
				>
					Đồng ý
				</button>
			</div>
		</div>
	</dialog>
</section>
