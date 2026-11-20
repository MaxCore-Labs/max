import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

function collectFiles(dir: string): string[] {
	const out: string[] = [];
	for (const entry of readdirSync(dir)) {
		const full = join(dir, entry);
		const st = statSync(full);
		if (st.isDirectory()) {
			out.push(...collectFiles(full));
		} else if (full.endsWith('.svelte') || full.endsWith('.ts')) {
			out.push(full);
		}
	}
	return out;
}

const colorRe = /#[0-9a-fA-F]{3,8}\b|\brgba?\s*\(|\bhsla?\s*\(/;
const lucideImportRe = /from\s+['"]([^'"]*lucide[^'"]*)['"]/g;

describe('design token & icon import discipline', () => {
	it('forbids hardcoded color literals and non-@lucide/svelte imports across src/lib and src/routes', () => {
		const roots = [join(process.cwd(), 'src', 'lib'), join(process.cwd(), 'src', 'routes')];
		for (const root of roots) {
			for (const file of collectFiles(root)) {
				if (file.endsWith('.d.ts') || file.endsWith('.test.ts') || file.endsWith('.spec.ts')) {
					continue;
				}
				const raw = readFileSync(file, 'utf8');
				expect(raw, `hardcoded color in ${file}`).not.toMatch(colorRe);

				for (const m of raw.matchAll(lucideImportRe)) {
					expect(m[1], `invalid lucide import in ${file}`).toMatch(/^@lucide\/svelte\/icons\//);
				}
			}
		}
	});
});
