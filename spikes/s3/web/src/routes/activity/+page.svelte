<script lang="ts">
	import CircleCheck from '@lucide/svelte/icons/circle-check';

	let dialogEl: HTMLDialogElement | undefined = $state();
	let triggerBtnEl: HTMLButtonElement | undefined = $state();
	let popoverEl: HTMLDivElement | undefined = $state();
	let popoverOpen = $state(false);
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
	// with full viewport clamping (horizontal + vertical flip) so anchored placement
	// works consistently without relying on CSS Anchor Positioning.
	function positionPopover(): void {
		if (!triggerBtnEl || !popoverEl) {
			return;
		}
		const margin = 8;
		const gap = 6;
		const rect = triggerBtnEl.getBoundingClientRect();
		const popRect = popoverEl.getBoundingClientRect();
		const popW = Math.ceil(popRect.width) || 216;
		const popH = Math.ceil(popRect.height) || 140;
		const vw = window.innerWidth;
		const vh = window.innerHeight;

		const maxLeft = Math.max(margin, vw - popW - margin);
		const left = Math.min(Math.max(margin, Math.round(rect.left)), maxLeft);

		const maxTop = Math.max(margin, vh - popH - margin);
		let top = Math.round(rect.bottom + gap);
		if (top > maxTop) {
			const flippedTop = Math.round(rect.top - gap - popH);
			top = flippedTop >= margin ? Math.min(flippedTop, maxTop) : maxTop;
		}

		popoverEl.style.inset = 'auto';
		popoverEl.style.position = 'fixed';
		popoverEl.style.margin = '0';
		popoverEl.style.left = `${left}px`;
		popoverEl.style.top = `${top}px`;
	}

	function handlePopoverToggle(ev: ToggleEvent): void {
		const isOpen = ev.newState === 'open';
		popoverOpen = isOpen;
		if (isOpen) {
			positionPopover();
			requestAnimationFrame(positionPopover);
		}
	}

	$effect(() => {
		if (!popoverOpen) {
			return;
		}
		const onResize = (): void => {
			positionPopover();
		};
		const onScroll = (): void => {
			if (!triggerBtnEl || !popoverEl) {
				return;
			}
			const rect = triggerBtnEl.getBoundingClientRect();
			if (rect.bottom < 0 || rect.top > window.innerHeight) {
				popoverEl.hidePopover();
				return;
			}
			positionPopover();
		};
		window.addEventListener('resize', onResize);
		window.addEventListener('scroll', onScroll, true);
		return () => {
			window.removeEventListener('resize', onResize);
			window.removeEventListener('scroll', onScroll, true);
		};
	});

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
