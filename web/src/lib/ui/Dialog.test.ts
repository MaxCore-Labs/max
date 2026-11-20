import { fireEvent, render } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import DialogFixture from '../../test-fixtures/DialogFixture.svelte';

describe('Dialog native <dialog>', () => {
	it('calls showModal() when open=true and sets open=false on cancel (Esc)', async () => {
		const showModalSpy = vi.spyOn(HTMLDialogElement.prototype, 'showModal');
		const { container, getByTestId } = render(DialogFixture, {
			props: { initialOpen: true, busy: false }
		});

		expect(showModalSpy).toHaveBeenCalledTimes(1);
		const dlg = container.querySelector('dialog') as HTMLDialogElement;
		expect(dlg).not.toBeNull();

		await fireEvent(dlg, new Event('cancel', { cancelable: true }));
		expect(getByTestId('open-state').textContent).toBe('false');
		showModalSpy.mockRestore();
	});

	it('prevents closing on cancel (Esc) when busy=true', async () => {
		const { container, getByTestId } = render(DialogFixture, {
			props: { initialOpen: true, busy: true }
		});
		const dlg = container.querySelector('dialog') as HTMLDialogElement;

		const cancelEv = new Event('cancel', { cancelable: true });
		dlg.dispatchEvent(cancelEv);
		expect(cancelEv.defaultPrevented).toBe(true);
		expect(getByTestId('open-state').textContent).toBe('true');
	});
});
