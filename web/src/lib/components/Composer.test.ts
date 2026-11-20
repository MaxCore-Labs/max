import { fireEvent, render } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import Composer from './Composer.svelte';

describe('Composer keyboard and empty guard', () => {
	it('blocks empty submission, sends on Enter, and allows newline on Shift+Enter', async () => {
		const onsend = vi.fn();
		const { container, getByText } = render(Composer, {
			props: { onsend }
		});

		const sendBtn = getByText('Gửi').closest('button')!;
		await fireEvent.click(sendBtn);
		expect(onsend).not.toHaveBeenCalled();

		const textarea = container.querySelector('textarea') as HTMLTextAreaElement;
		await fireEvent.input(textarea, { target: { value: '   ' } });
		await fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: false });
		expect(onsend).not.toHaveBeenCalled();

		await fireEvent.input(textarea, { target: { value: 'Dòng 1' } });
		await fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: true });
		expect(onsend).not.toHaveBeenCalled();

		await fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: false });
		expect(onsend).toHaveBeenCalledTimes(1);
		expect(onsend).toHaveBeenCalledWith('Dòng 1');
	});
});
