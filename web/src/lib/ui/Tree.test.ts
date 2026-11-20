import { fireEvent, render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import Tree from './Tree.svelte';

describe('Tree collapsible hierarchy', () => {
	it('does not render collapsed children and expands/collapses with ArrowRight/ArrowLeft', async () => {
		const nodes = [
			{
				id: 'team-1',
				label: 'Marketing',
				children: [{ id: 'agent-1', label: 'Nghiên cứu thị trường' }]
			}
		];

		const { queryByText, getByText } = render(Tree, {
			props: { nodes, expanded: [] }
		});

		expect(queryByText('Nghiên cứu thị trường')).toBeNull();

		const rootBtn = getByText('Marketing').closest('[role="treeitem"]') as HTMLElement;
		await fireEvent.keyDown(rootBtn, { key: 'ArrowRight' });
		expect(queryByText('Nghiên cứu thị trường')).not.toBeNull();

		await fireEvent.keyDown(rootBtn, { key: 'ArrowLeft' });
		expect(queryByText('Nghiên cứu thị trường')).toBeNull();
	});
});
