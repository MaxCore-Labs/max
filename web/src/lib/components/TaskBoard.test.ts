import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { Schemas } from '#lib/api/client.js';
import TaskBoard from './TaskBoard.svelte';

function makeTask(
	id: string,
	status: string,
	blockedReason?: string
): Schemas['Task'] {
	return {
		id,
		rootId: id,
		rootOrigin: 'owner',
		depth: 0,
		title: `Task ${id}`,
		description: '',
		doneCriteria: '',
		status,
		blockedReason: blockedReason ?? null,
		needsReview: false,
		reviewRounds: 0,
		idleRuns: 0,
		blockedBy: [],
		artifacts: [],
		createdAt: 1000,
		updatedAt: 1000
	};
}

describe('TaskBoard', () => {
	it('groups tasks into status columns and shows Vietnamese blocked reason text for all 4 reasons', () => {
		const tasks: Schemas['Task'][] = [
			makeTask('t-todo', 'todo'),
			makeTask('t-prog', 'in_progress'),
			makeTask('t-dep', 'blocked', 'dependency'),
			makeTask('t-stall', 'blocked', 'stalled'),
			makeTask('t-err', 'blocked', 'error'),
			makeTask('t-own', 'blocked', 'needs_owner'),
			makeTask('t-done', 'done')
		];

		render(TaskBoard, { props: { tasks } });

		expect(screen.getByTestId('task-col-todo')).toBeInTheDocument();
		expect(screen.getByTestId('task-col-in_progress')).toBeInTheDocument();
		expect(screen.getByTestId('task-col-blocked')).toBeInTheDocument();
		expect(screen.getByTestId('task-col-done')).toBeInTheDocument();

		const blockedCol = screen.getByTestId('task-col-blocked');
		expect(blockedCol.textContent).toContain('chờ phụ thuộc');
		expect(blockedCol.textContent).toContain('không tiến triển');
		expect(blockedCol.textContent).toContain('lỗi');
		expect(blockedCol.textContent).toContain('cần bạn');
	});
});
