<script lang="ts">
	import DOMPurify from 'dompurify';
	import { marked } from 'marked';

	interface Props {
		source: string;
		class?: string;
	}

	let { source, class: className = '' }: Props = $props();
	let hostEl: HTMLDivElement | undefined = $state();

	function buildSafeFragment(md: string): DocumentFragment {
		const rawHtml = marked.parse(md ?? '', { async: false, gfm: true, breaks: true });
		const clean = DOMPurify.sanitize(rawHtml, {
			USE_PROFILES: { html: true },
			FORBID_TAGS: ['script', 'style', 'iframe', 'object', 'embed', 'form', 'input', 'button'],
			FORBID_ATTR: ['onerror', 'onload', 'onclick', 'onmouseover', 'onfocus', 'style']
		});

		const tpl = document.createElement('template');
		tpl.innerHTML = clean;
		for (const anchor of tpl.content.querySelectorAll('a')) {
			const href = (anchor.getAttribute('href') ?? '').trim();
			if (/^(https?:)?\/\//i.test(href)) {
				anchor.setAttribute('rel', 'noopener noreferrer');
				anchor.setAttribute('target', '_blank');
			}
		}
		return tpl.content;
	}

	$effect(() => {
		if (!hostEl) return;
		hostEl.replaceChildren(buildSafeFragment(source));
	});
</script>

<div bind:this={hostEl} class="markdown-body space-y-2 leading-relaxed break-words {className}"></div>
