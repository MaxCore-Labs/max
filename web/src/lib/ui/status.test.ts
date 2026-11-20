import { describe, expect, it } from 'vitest';
import { statusMeta } from './status.js';

describe('statusMeta shared status mapping', () => {
	it('maps every run, task, approval, and agent status in openapi/schema without falling back to raw key', () => {
		const runs = [
			'running',
			'parked',
			'quota_stopped',
			'interrupted',
			'cancelled',
			'failed',
			'timed_out',
			'turn_limit',
			'succeeded'
		];
		for (const s of runs) {
			const m = statusMeta('run', s);
			expect(m.labelKey).not.toBe(s);
			expect(m.icon).toBeTruthy();
		}

		const tasks = ['todo', 'in_progress', 'in_review', 'blocked', 'waiting_owner', 'cancelled', 'done'];
		for (const s of tasks) {
			const m = statusMeta('task', s);
			expect(m.labelKey).not.toBe(s);
		}

		const approvals = ['pending', 'allowed', 'consumed', 'denied', 'expired'];
		for (const s of approvals) {
			const m = statusMeta('approval', s);
			expect(m.labelKey).not.toBe(s);
		}

		const agents = ['active', 'paused', 'quota_paused', 'archived'];
		for (const s of agents) {
			const m = statusMeta('agent', s);
			expect(m.labelKey).not.toBe(s);
		}
	});

	it('maps quota_stopped and interrupted to neutral, and running to spin=true', () => {
		expect(statusMeta('run', 'quota_stopped').tone).toBe('neutral');
		expect(statusMeta('run', 'interrupted').tone).toBe('neutral');
		expect(statusMeta('run', 'running').spin).toBe(true);
		expect(statusMeta('run', 'succeeded').spin).toBe(false);
	});

	it('maps blocked task reasons dependency|needs_owner|stalled|error to neutral|warning|error|error', () => {
		expect(statusMeta('task', 'blocked', 'dependency').tone).toBe('neutral');
		expect(statusMeta('task', 'blocked', 'needs_owner').tone).toBe('warning');
		expect(statusMeta('task', 'blocked', 'stalled').tone).toBe('error');
		expect(statusMeta('task', 'blocked', 'error').tone).toBe('error');
	});

	it('falls back to neutral tone and verbatim labelKey for unknown statuses', () => {
		const unknown = statusMeta('run', 'mystery_custom_state');
		expect(unknown.tone).toBe('neutral');
		expect(unknown.labelKey).toBe('mystery_custom_state');
		expect(unknown.spin).toBe(false);
	});
});
