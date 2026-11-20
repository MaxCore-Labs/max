import type { Component } from 'svelte';
import Ban from '@lucide/svelte/icons/ban';
import Check from '@lucide/svelte/icons/check';
import Circle from '@lucide/svelte/icons/circle';
import CircleCheck from '@lucide/svelte/icons/circle-check';
import CircleDot from '@lucide/svelte/icons/circle-dot';
import CircleEllipsis from '@lucide/svelte/icons/circle-ellipsis';
import CirclePause from '@lucide/svelte/icons/circle-pause';
import CircleQuestionMark from '@lucide/svelte/icons/circle-question-mark';
import CircleX from '@lucide/svelte/icons/circle-x';
import Clock from '@lucide/svelte/icons/clock';
import Hand from '@lucide/svelte/icons/hand';
import Hourglass from '@lucide/svelte/icons/hourglass';
import LoaderCircle from '@lucide/svelte/icons/loader-circle';
import MessageCircleQuestionMark from '@lucide/svelte/icons/message-circle-question-mark';
import OctagonX from '@lucide/svelte/icons/octagon-x';
import Pause from '@lucide/svelte/icons/pause';
import PlugZap from '@lucide/svelte/icons/plug-zap';
import ShieldAlert from '@lucide/svelte/icons/shield-alert';
import TimerOff from '@lucide/svelte/icons/timer-off';
import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
import Unplug from '@lucide/svelte/icons/unplug';
import X from '@lucide/svelte/icons/x';

export type StatusKind = 'run' | 'task' | 'approval' | 'agent' | 'engine' | 'tool';
export type StatusTone = 'neutral' | 'warning' | 'error' | 'success';

export interface StatusMeta {
	tone: StatusTone;
	icon: Component<{ size?: number; strokeWidth?: number; class?: string; 'aria-hidden'?: boolean | 'true' | 'false' }>;
	labelKey: string;
	spin: boolean;
}

const m = (tone: StatusTone, icon: StatusMeta['icon'], labelKey: string, spin = false): StatusMeta => ({
	tone,
	icon,
	labelKey,
	spin
});

const RUN_MAP: Record<string, StatusMeta> = {
	running: m('neutral', LoaderCircle, 'status.run.running', true),
	parked: m('neutral', CirclePause, 'status.run.parked'),
	quota_stopped: m('neutral', Hourglass, 'status.run.quota_stopped'),
	interrupted: m('neutral', Unplug, 'status.run.interrupted'),
	cancelled: m('neutral', Ban, 'status.run.cancelled'),
	failed: m('error', CircleX, 'status.run.failed'),
	timed_out: m('error', TimerOff, 'status.run.timed_out'),
	turn_limit: m('error', OctagonX, 'status.run.turn_limit'),
	succeeded: m('success', CircleCheck, 'status.run.succeeded')
};

const TASK_MAP: Record<string, StatusMeta> = {
	todo: m('neutral', Circle, 'status.task.todo'),
	in_progress: m('neutral', CircleDot, 'status.task.in_progress'),
	in_review: m('neutral', CircleEllipsis, 'status.task.in_review'),
	waiting_owner: m('warning', Hand, 'status.task.waiting_owner'),
	cancelled: m('neutral', Ban, 'status.task.cancelled'),
	done: m('success', CircleCheck, 'status.task.done')
};

const BLOCKED_MAP: Record<string, StatusMeta> = {
	dependency: m('neutral', Clock, 'status.task.blocked.dependency'),
	needs_owner: m('warning', Hand, 'status.task.blocked.needs_owner'),
	stalled: m('error', TriangleAlert, 'status.task.blocked.stalled'),
	error: m('error', CircleX, 'status.task.blocked.error')
};

const APPROVAL_MAP: Record<string, StatusMeta> = {
	pending: m('warning', ShieldAlert, 'status.approval.pending'),
	owner_question: m('warning', MessageCircleQuestionMark, 'status.approval.owner_question'),
	ambiguous: m('warning', CircleQuestionMark, 'status.approval.ambiguous'),
	allowed: m('success', Check, 'status.approval.allowed'),
	consumed: m('success', Check, 'status.approval.consumed'),
	denied: m('neutral', X, 'status.approval.denied'),
	expired: m('neutral', Clock, 'status.approval.expired')
};

const AGENT_MAP: Record<string, StatusMeta> = {
	active: m('success', CircleCheck, 'status.agent.active'),
	paused: m('neutral', Pause, 'status.agent.paused'),
	quota_paused: m('neutral', Hourglass, 'status.agent.quota_paused'),
	archived: m('neutral', Ban, 'status.agent.archived')
};

const ENGINE_MAP: Record<string, StatusMeta> = {
	ready: m('success', CircleCheck, 'status.engine.ready'),
	limited: m('neutral', Hourglass, 'status.engine.limited'),
	logged_out: m('error', PlugZap, 'status.engine.logged_out'),
	disabled: m('neutral', Ban, 'status.engine.disabled')
};

const TOOL_MAP: Record<string, StatusMeta> = {
	started: m('neutral', LoaderCircle, 'status.tool.started', true),
	done: m('success', CircleCheck, 'status.tool.done'),
	failed: m('error', CircleX, 'status.tool.failed'),
	ambiguous: m('warning', CircleQuestionMark, 'status.tool.ambiguous')
};

export function statusMeta(kind: StatusKind, status: string, reason?: string): StatusMeta {
	if (kind === 'task' && status === 'blocked') {
		return BLOCKED_MAP[reason ?? 'dependency'] ?? BLOCKED_MAP.dependency!;
	}
	const table =
		kind === 'run'
			? RUN_MAP
			: kind === 'task'
				? TASK_MAP
				: kind === 'approval'
					? APPROVAL_MAP
					: kind === 'agent'
						? AGENT_MAP
						: kind === 'engine'
							? ENGINE_MAP
							: TOOL_MAP;
	return table[status] ?? m('neutral', Circle, status, false);
}
