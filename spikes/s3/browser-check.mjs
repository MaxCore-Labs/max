import { spawn } from 'node:child_process';
import { existsSync, mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

function findChromeBinary() {
	if (process.env.CHROME_BIN && existsSync(process.env.CHROME_BIN)) return process.env.CHROME_BIN;
	const candidates =
		process.platform === 'win32'
			? [
					'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
					'C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe',
					'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe'
				]
			: process.platform === 'darwin'
				? ['/Applications/Google Chrome.app/Contents/MacOS/Google Chrome']
				: [
						'/usr/bin/google-chrome',
						'/usr/bin/google-chrome-stable',
						'/usr/bin/chromium',
						'/usr/bin/chromium-browser'
					];
	for (const c of candidates) if (existsSync(c)) return c;
	throw new Error('No Chrome/Chromium binary found; set CHROME_BIN');
}

function getFreePort() {
	return new Promise((resolve, reject) => {
		const srv = createServer();
		srv.listen(0, '127.0.0.1', () => {
			const addr = srv.address();
			const port = typeof addr === 'object' && addr ? addr.port : 0;
			srv.close((err) => (err ? reject(err) : resolve(port)));
		});
		srv.on('error', reject);
	});
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function waitForHttp(url, timeoutMs = 8000) {
	const deadline = Date.now() + timeoutMs;
	while (Date.now() < deadline) {
		try {
			const res = await fetch(url);
			if (res.ok) return await res.text();
		} catch {
			// retry
		}
		await sleep(100);
	}
	throw new Error(`Timed out waiting for ${url}`);
}

class CDPClient {
	constructor(wsUrl) {
		this.ws = new WebSocket(wsUrl);
		this.nextId = 1;
		this.pending = new Map();
		this.errors = [];
	}

	async connect() {
		await new Promise((resolve, reject) => {
			this.ws.addEventListener('open', () => resolve(), { once: true });
			this.ws.addEventListener('error', (e) => reject(e), { once: true });
		});
		this.ws.addEventListener('message', (ev) => {
			const msg = JSON.parse(String(ev.data));
			if (msg.id && this.pending.has(msg.id)) {
				const { resolve, reject } = this.pending.get(msg.id);
				this.pending.delete(msg.id);
				if (msg.error) reject(new Error(JSON.stringify(msg.error)));
				else resolve(msg.result);
				return;
			}
			if (msg.method === 'Runtime.exceptionThrown') {
				this.errors.push(`exception: ${JSON.stringify(msg.params.exceptionDetails?.text)}`);
			}
			if (msg.method === 'Runtime.consoleAPICalled' && msg.params?.type === 'error') {
				const text = (msg.params.args || []).map((a) => a.value ?? a.description ?? '').join(' ');
				this.errors.push(`console.error: ${text}`);
			}
		});
		await this.send('Page.enable');
		await this.send('Runtime.enable');
	}

	send(method, params = {}) {
		const id = this.nextId++;
		this.ws.send(JSON.stringify({ id, method, params }));
		return new Promise((resolve, reject) => this.pending.set(id, { resolve, reject }));
	}

	async evalJson(expression) {
		const res = await this.send('Runtime.evaluate', {
			expression: `JSON.stringify(${expression})`,
			returnByValue: true,
			awaitPromise: true
		});
		return res.result?.value ? JSON.parse(res.result.value) : null;
	}

	async pressEscape() {
		for (const type of ['rawKeyDown', 'keyUp']) {
			await this.send('Input.dispatchKeyEvent', {
				type,
				key: 'Escape',
				code: 'Escape',
				windowsVirtualKeyCode: 27,
				nativeVirtualKeyCode: 27
			});
		}
	}
}

const startS3Server = (binPath, port) =>
	spawn(binPath, ['-listen', `127.0.0.1:${port}`, '-tick-ms', '200'], { stdio: 'ignore' });

const percentile = (sorted, p) =>
	sorted[Math.max(0, Math.min(sorted.length - 1, Math.ceil((p / 100) * sorted.length) - 1))];

async function main() {
	const binPath = join(import.meta.dirname, process.platform === 'win32' ? 's3.exe' : 's3');
	if (!existsSync(binPath)) throw new Error(`Missing ${binPath}; build Go binary first`);
	const serverPort = await getFreePort();
	const cdpPort = await getFreePort();
	const profileDir = mkdtempSync(join(tmpdir(), 'max-s3-cdp-'));

	let s3Proc = startS3Server(binPath, serverPort);
	const chromeProc = spawn(
		findChromeBinary(),
		[
			'--headless=new',
			`--remote-debugging-port=${cdpPort}`,
			`--user-data-dir=${profileDir}`,
			'--window-size=1280,800',
			'--no-first-run',
			'--no-default-browser-check',
			'about:blank'
		],
		{ stdio: 'ignore' }
	);

	try {
		await waitForHttp(`http://127.0.0.1:${serverPort}/activity`);
		const versionInfo = JSON.parse(await waitForHttp(`http://127.0.0.1:${cdpPort}/json/version`));
		const targets = JSON.parse(await waitForHttp(`http://127.0.0.1:${cdpPort}/json/list`));
		const cdp = new CDPClient(targets.find((t) => t.type === 'page').webSocketDebuggerUrl);
		await cdp.connect();

		await cdp.send('Page.navigate', { url: `http://127.0.0.1:${serverPort}/activity` });
		let tokenCheck = null;
		for (let i = 0; i < 40 && !tokenCheck; i++) {
			tokenCheck = await cdp.evalJson(`(() => {
				const btn = document.querySelector('[data-testid="primary-btn"]');
				if (!btn) return null;
				const cs = getComputedStyle(btn);
				return {
					pathname: location.pathname,
					bgColor: cs.backgroundColor,
					fgColor: cs.color,
					svgWidth: btn.querySelector('svg')?.getAttribute('width') ?? null
				};
			})()`);
			if (!tokenCheck) await sleep(100);
		}
		if (!tokenCheck || tokenCheck.bgColor !== 'rgb(24, 24, 24)') {
			throw new Error(`Token check failed: ${JSON.stringify(tokenCheck)}`);
		}
		console.log(`[CDP 1] Browser=${versionInfo.Browser} /activity tokens:`, JSON.stringify(tokenCheck));

		await cdp.evalJson(`(document.querySelector('[data-testid="primary-btn"]').click(), true)`);
		await sleep(80);
		const dlgBefore = await cdp.evalJson(`document.querySelector('[data-testid="demo-dialog"]').open`);
		await cdp.pressEscape();
		await sleep(80);
		const dlgAfter = await cdp.evalJson(`document.querySelector('[data-testid="demo-dialog"]').open`);
		if (!dlgBefore || dlgAfter) throw new Error(`<dialog> Esc failed: ${dlgBefore} -> ${dlgAfter}`);
		console.log(`[CDP 2] <dialog> showModal() -> open=${dlgBefore} | after Esc -> open=${dlgAfter}`);

		await cdp.send('Emulation.setDeviceMetricsOverride', {
			width: 360,
			height: 260,
			deviceScaleFactor: 1,
			mobile: false
		});
		await sleep(100);
		await cdp.evalJson(`(document.querySelector('[data-testid="popover-trigger"]').click(), true)`);
		await sleep(120);
		const popState = await cdp.evalJson(`(() => {
			const el = document.querySelector('[data-testid="demo-popover"]');
			const r = el.getBoundingClientRect();
			return {
				open: el.matches(':popover-open'),
				left: Math.round(r.left),
				top: Math.round(r.top),
				right: Math.round(r.right),
				bottom: Math.round(r.bottom),
				vw: window.innerWidth,
				vh: window.innerHeight
			};
		})()`);
		await cdp.pressEscape();
		await sleep(80);
		const popAfter = await cdp.evalJson(
			`document.querySelector('[data-testid="demo-popover"]').matches(':popover-open')`
		);
		await cdp.send('Emulation.clearDeviceMetricsOverride');
		if (
			!popState?.open ||
			popAfter ||
			popState.left < 8 ||
			popState.top < 8 ||
			popState.right > popState.vw - 8 ||
			popState.bottom > popState.vh - 8
		) {
			throw new Error(`[popover] check failed: state=${JSON.stringify(popState)} after=${popAfter}`);
		}
		console.log(`[CDP 3] [popover] clamped & Esc:`, JSON.stringify({ ...popState, afterEsc: popAfter }));

		await cdp.send('Page.navigate', { url: `http://127.0.0.1:${serverPort}/` });
		const samples = [];
		let lastSeenSeq = 0;
		const deadline = Date.now() + 12000;
		while (samples.length < 20 && Date.now() < deadline) {
			const snap = await cdp.evalJson(`(() => {
				const panel = document.querySelector('[data-testid="sse-panel"]');
				const seqEl = document.querySelector('[data-testid="latest-seq"]');
				const latEl = document.querySelector('[data-testid="latest-latency"]');
				if (!panel || !seqEl || !latEl) return null;
				return {
					bootId: panel.dataset.bootId,
					seq: Number(seqEl.textContent.trim().replace('#', '')),
					latMs: Number(latEl.textContent.trim().replace(' ms', ''))
				};
			})()`);
			if (snap && snap.seq > lastSeenSeq) {
				lastSeenSeq = snap.seq;
				samples.push(snap);
			}
			await sleep(60);
		}
		if (samples.length < 20) throw new Error(`Expected 20 SSE samples, got ${samples.length}`);
		const sortedLat = samples.map((s) => s.latMs).sort((a, b) => a - b);
		const stats = {
			count: samples.length,
			minMs: sortedLat[0],
			medianMs: percentile(sortedLat, 50),
			p95Ms: percentile(sortedLat, 95),
			maxMs: sortedLat[sortedLat.length - 1]
		};
		if (stats.p95Ms >= 200 || stats.maxMs >= 200) throw new Error(`SSE UI latency >= 200ms`);
		console.log(`[CDP 4] SSE -> $state -> DOM latency (N=${stats.count}):`, JSON.stringify(stats));

		const bootBefore = samples[samples.length - 1].bootId;
		s3Proc.kill();
		await new Promise((r) => s3Proc.once('exit', r));
		await sleep(350);
		s3Proc = startS3Server(binPath, serverPort);
		await waitForHttp(`http://127.0.0.1:${serverPort}/activity`);

		let recovered = null;
		for (let i = 0; i < 60 && !recovered; i++) {
			const snap = await cdp.evalJson(`(() => {
				const panel = document.querySelector('[data-testid="sse-panel"]');
				const seqEl = document.querySelector('[data-testid="latest-seq"]');
				if (!panel || !seqEl) return null;
				return {
					bootId: panel.dataset.bootId,
					reconnectCount: Number(panel.dataset.reconnectCount || '0'),
					seq: Number(seqEl.textContent.trim().replace('#', '')),
					rows: document.querySelectorAll('tbody tr').length
				};
			})()`);
			if (snap && snap.bootId && snap.bootId !== bootBefore && snap.seq >= 2) recovered = snap;
			else await sleep(150);
		}
		if (!recovered) throw new Error('SSE failed to recover cleanly after Go server restart');
		if (cdp.errors.length > 0) throw new Error(`Runtime errors: ${cdp.errors.join(' | ')}`);
		console.log(`[CDP 5] Server restart reconnect clean (0 runtime errors):`, JSON.stringify(recovered));
		cdp.ws.close();
	} finally {
		s3Proc.kill();
		chromeProc.kill();
		await sleep(200);
		rmSync(profileDir, { recursive: true, force: true });
	}
}

await main();
