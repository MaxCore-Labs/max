import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { svelteTesting } from '@testing-library/svelte/vite';
import { defineConfig } from 'vitest/config';

export default defineConfig({
	plugins: [
		tailwindcss(),
		sveltekit({
			adapter: adapter({
				pages: 'build',
				assets: 'build',
				fallback: '200.html',
				precompress: true,
				strict: true
			})
		}),
		svelteTesting()
	],
	test: {
		environment: 'jsdom',
		setupFiles: ['./src/test-setup.ts'],
		include: ['src/**/*.{test,spec}.ts']
	}
});
