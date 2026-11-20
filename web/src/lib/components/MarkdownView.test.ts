import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import MarkdownView from './MarkdownView.svelte';

describe('MarkdownView sanitization', () => {
	it('strips <script> tags and inline event handlers like onerror', () => {
		const malicious = [
			'Xin chào **Max**!',
			'<script>alert(1)</script>',
			'<img src="x" onerror="alert(2)">',
			'[Tài liệu ngoài](https://example.com/docs)'
		].join('\n\n');

		const { container } = render(MarkdownView, { props: { source: malicious } });

		expect(container.querySelector('script')).toBeNull();
		expect(container.innerHTML).not.toContain('alert(1)');
		expect(container.innerHTML).not.toContain('onerror');

		const img = container.querySelector('img');
		if (img) {
			expect(img.getAttribute('onerror')).toBeNull();
		}

		const link = container.querySelector('a[href="https://example.com/docs"]');
		expect(link).not.toBeNull();
		expect(link?.getAttribute('rel')).toBe('noopener noreferrer');
	});
});
