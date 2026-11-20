<script lang="ts">
	import { goto } from '$app/navigation';
	import { ApiError } from '#lib/api/client.js';
	import { t } from '#lib/i18n/vi.js';
	import { session } from '#lib/state/session.svelte.js';
	import { Button, Card, Field, Input } from '#lib/ui/index.js';

	let password = $state('');
	let errorMsg = $state('');

	async function handleSubmit(ev: SubmitEvent): Promise<void> {
		ev.preventDefault();
		if (!password || session.loading) return;
		errorMsg = '';
		try {
			await session.login(password);
			await goto('/');
		} catch (err) {
			if (err instanceof ApiError && err.status === 401) {
				errorMsg = t('auth.invalid');
			} else if (err instanceof Error) {
				errorMsg = err.message;
			} else {
				errorMsg = t('auth.invalid');
			}
		}
	}
</script>

<div class="flex min-h-screen items-center justify-center bg-background p-4">
	<Card title={t('auth.title')} class="w-full max-w-sm">
		<p class="-mt-2 mb-4 text-sm text-muted">{t('auth.subtitle')}</p>
		<form onsubmit={handleSubmit} class="flex flex-col gap-4">
			<Field label={t('auth.password')} error={errorMsg}>
				<Input type="password" bind:value={password} autocomplete="current-password" />
			</Field>
			<Button
				type="submit"
				variant="primary"
				loading={session.loading}
				disabled={password.length === 0}
			>
				{t('auth.submit')}
			</Button>
		</form>
	</Card>
</div>
