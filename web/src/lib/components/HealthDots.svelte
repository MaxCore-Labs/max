<script lang="ts">
	import type { Schemas } from '#lib/api/client.js';

	interface Props {
		engines: Schemas['EngineStatusItem'][];
		deps?: Record<string, string>;
		class?: string;
	}

	let { engines, deps = {}, class: className = '' }: Props = $props();

	function fmtTime(ms: number): string {
		const d = new Date(ms);
		return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
	}
</script>

<div class="flex flex-wrap items-center gap-2 {className}">
	{#each engines as eng (eng.name)}
		{@const isLimited = Boolean(eng.limitedUntil && eng.limitedUntil > Date.now())}
		{@const badAuth = eng.enabled && (!eng.installed || !eng.loggedIn)}
		<span
			class="inline-flex h-8 items-center gap-2 rounded-[var(--radius-full)] border border-border-strong bg-surface px-3 text-xs whitespace-nowrap text-foreground"
		>
			<span
				class="h-2 w-2 rounded-[var(--radius-full)] {badAuth
					? 'bg-error-strong'
					: isLimited || !eng.enabled
						? 'bg-neutral'
						: 'bg-success'}"
			></span>
			<b class="font-semibold">{eng.name}</b>
			{#if badAuth}
				<span class="text-muted">cần đăng nhập lại</span>
			{:else if isLimited && eng.limitedUntil}
				<span class="text-muted">mở lại {fmtTime(eng.limitedUntil)}</span>
			{:else if !eng.enabled}
				<span class="text-muted">đã tắt</span>
			{:else}
				<span class="text-muted tabular-nums">{eng.used}/{eng.slots} chỗ</span>
			{/if}
		</span>
	{/each}

	{#each Object.entries(deps) as [depName, depState] (depName)}
		<span
			class="inline-flex h-8 items-center gap-2 rounded-[var(--radius-full)] border border-border-strong bg-surface px-3 text-xs whitespace-nowrap text-muted"
		>
			<span
				class="h-2 w-2 rounded-[var(--radius-full)] {depState === 'ok'
					? 'bg-success'
					: 'bg-error-strong'}"
			></span>
			<span class="font-mono">{depName}</span>
		</span>
	{/each}
</div>
