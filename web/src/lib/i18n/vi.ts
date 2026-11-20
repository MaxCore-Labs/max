export const vi: Record<string, string> = {
	'app.title': 'Max',
	'nav.main': 'Điều hướng chính',
	'nav.mobile': 'Điều hướng trên điện thoại',
	'nav.chat': 'Chat',
	'nav.inbox': 'Inbox',
	'nav.agents': 'Agents',
	'nav.tasks': 'Việc',
	'nav.activity': 'Hoạt động',
	'nav.knowledge': 'Tri thức',
	'nav.usage': 'Mức dùng',
	'nav.system': 'Hệ thống',
	'nav.more': 'Thêm',

	'common.allow': 'Cho phép',
	'common.deny': 'Từ chối',
	'common.cancel': 'Huỷ',
	'common.save': 'Lưu',
	'common.send': 'Gửi',
	'common.reply': 'Trả lời',
	'common.reset': 'Làm mới phiên',
	'common.clear_search': 'Xoá tìm kiếm',
	'common.close': 'Đóng',
	'common.loading': 'Đang tải…',
	'common.empty': 'Không có mục nào.',

	// Status mapping labels (matching design-system.html verbatim)
	'status.run.running': 'Đang chạy',
	'status.run.parked': 'Tạm nghỉ',
	'status.run.quota_stopped': 'Hết hạn mức',
	'status.run.interrupted': 'Bị ngắt',
	'status.run.cancelled': 'Đã huỷ',
	'status.run.failed': 'Lỗi',
	'status.run.timed_out': 'Quá giờ',
	'status.run.turn_limit': 'Hết lượt',
	'status.run.succeeded': 'Xong',

	'status.task.todo': 'Cần làm',
	'status.task.in_progress': 'Đang làm',
	'status.task.in_review': 'Đang review',
	'status.task.blocked.dependency': 'Chờ việc khác',
	'status.task.waiting_owner': 'Cần bạn',
	'status.task.blocked.needs_owner': 'Cần bạn',
	'status.task.blocked.stalled': 'Bị kẹt',
	'status.task.blocked.error': 'Lỗi',
	'status.task.cancelled': 'Đã huỷ',
	'status.task.done': 'Xong',

	'status.approval.pending': 'Chờ duyệt',
	'status.approval.owner_question': 'Agent hỏi',
	'status.approval.ambiguous': 'Chưa rõ đã gửi',
	'status.approval.allowed': 'Đã cho phép',
	'status.approval.consumed': 'Đã cho phép',
	'status.approval.denied': 'Đã từ chối',
	'status.approval.expired': 'Hết hạn',

	'status.agent.active': 'Hoạt động',
	'status.agent.paused': 'Tạm dừng',
	'status.agent.quota_paused': 'Hết phần hạn mức',
	'status.agent.archived': 'Đã lưu trữ',

	'status.engine.ready': 'Sẵn sàng',
	'status.engine.limited': 'Hết hạn mức',
	'status.engine.logged_out': 'Mất đăng nhập',
	'status.engine.disabled': 'Đã tắt',

	'status.tool.ambiguous': 'Chưa rõ đã gửi',
	'status.tool.done': 'Xong',
	'status.tool.failed': 'Lỗi',
	'status.tool.started': 'Đang chạy',

	// Auth / Login
	'auth.title': 'Đăng nhập Max',
	'auth.subtitle': 'Nhập mật khẩu chủ nhân để vào bảng điều khiển.',
	'auth.password': 'Mật khẩu',
	'auth.submit': 'Đăng nhập',
	'auth.logout': 'Đăng xuất',
	'auth.invalid': 'Mật khẩu không đúng.',

	// Chat
	'chat.threads': 'Cuộc trò chuyện',
	'chat.new_thread': 'Cuộc trò chuyện mới',
	'chat.placeholder': 'Nhắn cho agent… (Enter để gửi, Shift+Enter xuống dòng)',
	'chat.empty': 'Bắt đầu cuộc trò chuyện với Chánh văn phòng.',
	'chat.busy_text': 'Đang bận: {task} · {minutes} phút',
	'chat.stop_busy': 'Dừng việc đó',

	// Inbox
	'inbox.title': 'Cần bạn xử lý',
	'inbox.empty': 'Không có việc nào đang chờ bạn.',
	'inbox.mark_read': 'Đã xem',
	'inbox.answer_label': 'Câu trả lời của bạn',
	'inbox.answer_placeholder': 'Nhập câu trả lời…',

	// Usage & Quota
	'usage.title': 'Mức dùng & Hạn mức',
	'quota.learning': 'Đang học',
	'quota.learning_note': 'Chưa gặp giới hạn nào. Đã dùng {tokens} token.',
	'quota.reopen_at': 'Mở lại {time}',
	'quota.waiting_note': '{count} việc đang chờ, tự chạy lại khi mở.',
	'quota.normal_note': 'Việc nền dừng ở {pct}%. Cửa sổ mới lúc {time}.',
	'quota.short_window': '{engine} · cửa sổ 5 giờ',
	'quota.week_window': '{engine} · cửa sổ tuần',

	// System & Engine Settings
	'system.title': 'Hệ thống',
	'system.engines': 'Cài đặt CLI Engine',
	'system.deps': 'Sức khoẻ phụ thuộc',
	'system.slots_label': 'Số chỗ song song',
	'system.slots_range_err': 'Số chỗ phải từ 1 đến 8.',
	'system.last_engine_err': 'Phải giữ ít nhất 1 engine đang bật.',
	'system.login_hint': 'Chưa đăng nhập. Chạy lệnh: {cmd}',
	'system.enabled': 'Đang bật',
	'system.disabled': 'Đã tắt'
};

export function t(key: string, params?: Record<string, string | number>): string {
	const raw = vi[key] ?? key;
	if (!params) return raw;
	return raw.replace(/\{(\w+)\}/g, (_, k: string) =>
		params[k] !== undefined ? String(params[k]) : `{${k}}`
	);
}
