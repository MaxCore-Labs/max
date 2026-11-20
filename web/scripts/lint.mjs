import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';

const ROOT = process.cwd();
const SRC_DIR = join(ROOT, 'src');
const MAX_LINES = 300;
const MAX_UI_TOTAL = 1200;

const colorLiteralRe = /#[0-9a-fA-F]{3,8}\b|\brgba?\s*\(|\bhsla?\s*\(/;
const legacySvelteStoreRe = /from\s+['"]svelte\/store['"]/;
const legacyExportLetRe = /^\s*export\s+let\s+/m;
const legacyReactiveRe = /^\s*\$:\s+/m;
const legacySlotRe = /<slot[\s/>]/;
const lucideImportRe = /from\s+['"]([^'"]*lucide[^'"]*)['"]/g;

function walk(dir) {
	const out = [];
	for (const entry of readdirSync(dir)) {
		const full = join(dir, entry);
		const st = statSync(full);
		if (st.isDirectory()) {
			out.push(...walk(full));
		} else if (full.endsWith('.svelte') || full.endsWith('.ts')) {
			out.push(full);
		}
	}
	return out;
}

let errors = 0;
let uiTotalLines = 0;

for (const file of walk(SRC_DIR)) {
	const rel = relative(ROOT, file).replace(/\\/g, '/');
	if (rel.endsWith('.d.ts')) continue;

	const raw = readFileSync(file, 'utf8');
	const lines = raw.split(/\r?\n/).length;
	const isTest = rel.endsWith('.test.ts') || rel.endsWith('.spec.ts');

	if (lines > MAX_LINES) {
		console.error(`ERROR: ${rel} exceeds ${MAX_LINES} lines (${lines})`);
		errors++;
	}
	if (!isTest && rel.startsWith('src/lib/ui/')) {
		uiTotalLines += lines;
	}

	if (!isTest) {
		if (legacySvelteStoreRe.test(raw)) {
			console.error(`ERROR: ${rel} imports forbidden svelte/store`);
			errors++;
		}
		if (file.endsWith('.svelte')) {
			if (legacyExportLetRe.test(raw)) {
				console.error(`ERROR: ${rel} uses forbidden legacy 'export let'`);
				errors++;
			}
			if (legacyReactiveRe.test(raw)) {
				console.error(`ERROR: ${rel} uses forbidden legacy '$:' reactive label`);
				errors++;
			}
			if (legacySlotRe.test(raw)) {
				console.error(`ERROR: ${rel} uses forbidden legacy '<slot>'`);
				errors++;
			}
		}
		if ((rel.startsWith('src/lib/') || rel.startsWith('src/routes/')) && colorLiteralRe.test(raw)) {
			console.error(`ERROR: ${rel} contains hardcoded color literal (must use tokens.css)`);
			errors++;
		}
		for (const m of raw.matchAll(lucideImportRe)) {
			const pkg = m[1];
			if (!pkg.startsWith('@lucide/svelte/icons/')) {
				console.error(`ERROR: ${rel} imports '${pkg}'; must use '@lucide/svelte/icons/<name>'`);
				errors++;
			}
		}
	}
}

if (uiTotalLines > MAX_UI_TOTAL) {
	console.error(`ERROR: src/lib/ui total lines (${uiTotalLines}) exceeds ${MAX_UI_TOTAL}`);
	errors++;
}

if (errors > 0) {
	process.exit(1);
}
console.log(`Web lint passed (ui lines: ${uiTotalLines}/${MAX_UI_TOTAL}).`);
