<script lang="ts">
	import { t } from '#lib/i18n/vi.js';
	import { statusMeta, type StatusKind, type StatusTone } from './status.js';

	interface Props {
		kind: StatusKind;
		status: string;
		reason?: string;
		class?: string;
	}

	let { kind, status, reason, class: className = '' }: Props = $props();

	const TONE_CLASSES: Record<StatusTone, string> = {
		neutral: 'bg-neutral-bg text-neutral',
		warning: 'bg-warning-bg text-warning',
		error: 'bg-error-bg text-error-strong',
		success: 'bg-success-bg text-success'
	};

	let meta = $derived(statusMeta(kind, status, reason));
</script>

<span
	class="inline-flex items-center gap-1.5 whitespace-nowrap rounded-[var(--radius-full)] px-2.5 py-1 text-xs leading-4 font-medium ring-1 ring-inset ring-foreground/5 {TONE_CLASSES[
		meta.tone
	]} {className}"
>
	<meta.icon
		size={14}
		strokeWidth={2}
		aria-hidden="true"
		class="shrink-0 {meta.spin ? 'animate-spin motion-reduce:animate-none' : ''}"
	/>
	<span>{t(meta.labelKey)}</span>
</span>
