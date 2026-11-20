import { render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import ConnectionForm from './ConnectionForm.svelte';

describe('ConnectionForm', () => {
	it('never displays existing secret values when editing a configured connection', () => {
		render(ConnectionForm, {
			props: {
				existing: {
					name: 'crm',
					kind: 'http',
					configured: true,
					enabled: true,
					tools: [{ name: 'create_lead', write: true, targetField: 'email' }]
				},
				onsave: vi.fn()
			}
		});

		const secretField = screen.getByLabelText(/Cấu hình bí mật/i) as HTMLTextAreaElement | HTMLInputElement;
		expect(secretField.value).toBe('');
		expect(screen.getByText(/Đã cấu hình/i)).toBeInTheDocument();
	});
});
