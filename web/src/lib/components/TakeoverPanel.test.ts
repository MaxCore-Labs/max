import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import TakeoverPanel from './TakeoverPanel.svelte';

describe('TakeoverPanel', () => {
	it('displays loopback takeover link and triggers stop callback', async () => {
		const onstop = vi.fn();
		render(TakeoverPanel, {
			props: {
				active: true,
				url: 'http://127.0.0.1:49152/vnc.html',
				origin: 'https://portal.example.com',
				onstop
			}
		});

		const link = screen.getByRole('link', { name: /Mở phiên tiếp quản/i }) as HTMLAnchorElement;
		expect(link.href).toContain('127.0.0.1:49152');
		expect(screen.getByText(/portal\.example\.com/i)).toBeInTheDocument();

		const stopBtn = screen.getByRole('button', { name: /Kết thúc tiếp quản/i });
		await fireEvent.click(stopBtn);
		expect(onstop).toHaveBeenCalledTimes(1);
	});
});
