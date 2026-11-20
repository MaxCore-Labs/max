<script lang="ts">
	import { t } from '#lib/i18n/vi.js';
	import { Button, Textarea } from '#lib/ui/index.js';
	import Send from '@lucide/svelte/icons/send';

	interface Props {
		disabled?: boolean;
		onsend: (content: string) => void | Promise<void>;
		class?: string;
	}

	let { disabled = false, onsend, class: className = '' }: Props = $props();
	let draft = $state('');
	let sending = $state(false);

	async function submit(): Promise<void> {
		const text = draft.trim();
		if (!text || disabled || sending) return;
		sending = true;
		draft = '';
		try {
			await onsend(text);
		} finally {
			sending = false;
		}
	}

	function handleKeyDown(ev: KeyboardEvent): void {
		if (ev.key === 'Enter' && !ev.shiftKey) {
			ev.preventDefault();
			void submit();
		}
	}
</script>

<form
	onsubmit={(ev) => {
		ev.preventDefault();
		void submit();
	}}
	class="flex items-end gap-2 {className}"
>
	<div class="min-w-0 flex-1">
		<Textarea
			bind:value={draft}
			rows={2}
			placeholder={t('chat.placeholder')}
			{disabled}
			onkeydown={handleKeyDown}
		/>
	</div>
	<Button
		type="submit"
		variant="primary"
		icon={Send}
		loading={sending}
		disabled={disabled || draft.trim().length === 0}
	>
		{t('common.send')}
	</Button>
</form>
