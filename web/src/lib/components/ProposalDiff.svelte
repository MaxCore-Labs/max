<script lang="ts">
	import { Button, Card } from '#lib/ui/index.js';

	interface Props {
		title: string;
		targetPath: string;
		diff: string;
		loading?: boolean;
		ondecide: (approve: boolean) => void;
	}

	let { title, targetPath, diff, loading = false, ondecide }: Props = $props();
	let lines = $derived(diff.replaceAll('\r\n', '\n').split('\n'));

	function lineClass(line: string): string {
		if (line.startsWith('+++') || line.startsWith('---')) return 'text-muted';
		if (line.startsWith('+')) return 'bg-success-bg text-success';
		if (line.startsWith('-')) return 'bg-error-bg text-error-strong';
		return 'text-foreground';
	}
</script>

<Card {title}>
	<div class="flex flex-col gap-3">
		<div class="font-mono text-xs text-muted">{targetPath}</div>
		<pre class="overflow-x-auto rounded-lg border border-border-strong bg-background p-3 font-mono text-xs">{#each lines as line, idx (idx)}<div class={lineClass(line)}>{line}</div>{/each}</pre>
		<div class="flex justify-end gap-2">
			<Button variant="outline" disabled={loading} onclick={() => ondecide(false)}>Từ chối</Button>
			<Button variant="primary" {loading} onclick={() => ondecide(true)}>Áp dụng</Button>
		</div>
	</div>
</Card>
