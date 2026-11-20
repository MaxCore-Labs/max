<script lang="ts">
	import { Button, Card } from '#lib/ui/index.js';

	interface Props {
		title: string;
		slug: string;
		description: string;
		loading?: boolean;
		ondecide: (approve: boolean) => void;
	}

	let { title, slug, description, loading = false, ondecide }: Props = $props();
</script>

<Card {title}>
	<div class="flex flex-col gap-3">
		<div class="font-mono text-xs text-muted">skills/_proposed/{slug}/SKILL.md</div>
		<p class="text-sm text-foreground">{description}</p>
		<div class="flex justify-end gap-2">
			<Button variant="outline" disabled={loading} onclick={() => ondecide(false)}>Từ chối</Button>
			<Button variant="primary" {loading} onclick={() => ondecide(true)}>Duyệt skill</Button>
		</div>
	</div>
</Card>
