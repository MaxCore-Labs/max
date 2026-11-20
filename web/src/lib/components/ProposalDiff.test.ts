import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import ProposalDiff from './ProposalDiff.svelte';

describe('ProposalDiff', () => {
	it('renders unified diff lines and triggers approve/reject actions', async () => {
		const ondecide = vi.fn();
		render(ProposalDiff, {
			props: {
				title: 'Cập nhật quy tắc trích nguồn',
				targetPath: 'agents/chief-of-staff/AGENT.md',
				diff: '--- a/AGENT.md\n+++ b/AGENT.md\n@@ -1 +1,2 @@\n+Luôn đính kèm nguồn tham khảo.',
				ondecide
			}
		});

		expect(screen.getByText('Cập nhật quy tắc trích nguồn')).toBeInTheDocument();
		expect(screen.getByText('+Luôn đính kèm nguồn tham khảo.')).toBeInTheDocument();

		const applyBtn = screen.getByRole('button', { name: /Áp dụng/i });
		const rejectBtn = screen.getByRole('button', { name: /Từ chối/i });

		await fireEvent.click(applyBtn);
		expect(ondecide).toHaveBeenCalledWith(true);

		await fireEvent.click(rejectBtn);
		expect(ondecide).toHaveBeenCalledWith(false);
	});
});
