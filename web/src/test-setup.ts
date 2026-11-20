import '@testing-library/jest-dom/vitest';

if (typeof HTMLDialogElement !== 'undefined') {
	if (!HTMLDialogElement.prototype.showModal) {
		HTMLDialogElement.prototype.showModal = function showModal() {
			this.setAttribute('open', '');
		};
	}
	if (!HTMLDialogElement.prototype.close) {
		HTMLDialogElement.prototype.close = function close(returnValue?: string) {
			if (returnValue !== undefined) {
				this.returnValue = returnValue;
			}
			this.removeAttribute('open');
			this.dispatchEvent(new Event('close'));
		};
	}
}

if (typeof HTMLElement !== 'undefined') {
	if (!HTMLElement.prototype.showPopover) {
		HTMLElement.prototype.showPopover = function showPopover() {
			this.setAttribute('data-popover-open', 'true');
			const ev = new Event('toggle') as Event & { newState: string; oldState: string };
			ev.oldState = 'closed';
			ev.newState = 'open';
			this.dispatchEvent(ev);
		};
	}
	if (!HTMLElement.prototype.hidePopover) {
		HTMLElement.prototype.hidePopover = function hidePopover() {
			this.removeAttribute('data-popover-open');
			const ev = new Event('toggle') as Event & { newState: string; oldState: string };
			ev.oldState = 'open';
			ev.newState = 'closed';
			this.dispatchEvent(ev);
		};
	}
	if (!HTMLElement.prototype.togglePopover) {
		HTMLElement.prototype.togglePopover = function togglePopover(force?: boolean) {
			const isOpen = this.hasAttribute('data-popover-open');
			const next = force !== undefined ? force : !isOpen;
			if (next) {
				this.showPopover();
			} else {
				this.hidePopover();
			}
			return next;
		};
	}
}
