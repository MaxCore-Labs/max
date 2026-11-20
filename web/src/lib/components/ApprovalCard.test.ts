import { fireEvent, render } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, type Schemas } from '#lib/api/client.js';
import ApprovalCard from './ApprovalCard.svelte';

describe('ApprovalCard double-click guard and state transition', () => {
	afterEach(() => {
		vi.restoreAllMocks();
	});

	it('sends only 1 decideApproval request on rapid double-click and hides buttons once decided', async () => {
		let resolveReq!: (val: Schemas['Approval']) => void;
		const decideSpy = vi.spyOn(api, 'decideApproval').mockImplementation(
			() =>
				new Promise<Schemas['Approval']>((resolve) => {
					resolveReq = resolve;
				})
		);

		const approval = {
			id: 'ap-1',
			runId: 'run-1',
			agentId: 'sales-outreach',
			conversationId: 'conv-1',
			toolName: 'gmail_send',
			preview: 'Người nhận: mua-hang@minhphat.example\nTiêu đề: Báo giá gói Doanh nghiệp',
			reason: 'Gửi email ra ngoài cần chủ nhân duyệt',
			status: 'pending',
			requestedAt: Date.now() - 60_000,
			expiresAt: Date.now() + 82_800_000
		};

		const { getByText, queryByText } = render(ApprovalCard, {
			props: { approval }
		});

		const allowBtn = getByText('Cho phép').closest('button')!;
		await fireEvent.click(allowBtn);
		await fireEvent.click(allowBtn);

		expect(decideSpy).toHaveBeenCalledTimes(1);
		expect(decideSpy).toHaveBeenCalledWith('ap-1', { allow: true });

		resolveReq({ ...approval, status: 'allowed', decidedAt: Date.now() });
		await vi.waitFor(() => {
			expect(getByText('Đã cho phép')).toBeTruthy();
		});
		expect(queryByText('Cho phép')).toBeNull();
		expect(queryByText('Từ chối')).toBeNull();
	});
});
