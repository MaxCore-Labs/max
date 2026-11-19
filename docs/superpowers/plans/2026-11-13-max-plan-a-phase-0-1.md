# Max - Kế hoạch triển khai A: Phase 0 (spike) và Phase 1 (lõi một agent trên `agy`)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Sau kế hoạch này có một hệ chạy được: chủ nhân chat với một agent `chief-of-staff` qua UI, agent chạy bằng tiến trình `agy`, mọi lời gọi tool qua cổng quyền, việc không mất khi tiến trình chết, và hạn mức gói được điều tiết.

**Architecture:** Một binary Go (hexagonal: `domain` thuần, `app` giữ use case và port, `adapter` hiện thực port) với SQLite nhúng. Mỗi agent là một actor có hộp thư bền; mỗi run là một tiến trình CLI luôn kết thúc; việc "chờ" nằm ở conversation. UI là SPA Svelte 5 nhúng vào binary, nói chuyện qua REST + SSE.

**Tech Stack:** Go 1.27 (`CGO_ENABLED=0`), `modernc.org/sqlite` v1.62, `sqlc` v1.31, `goose` v3.30, `oapi-codegen` v2.8, `modelcontextprotocol/go-sdk` v1.8, `robfig/cron/v3`, `google/uuid`; Svelte 5.57 + SvelteKit 3.2 + adapter-static 4, Tailwind 4, `@lucide/svelte` 1.72, `@fontsource/inter`, `openapi-typescript` 7.13, bun; CLI `agy` 1.5.12.

**Spec:** `docs/superpowers/specs/2026-11-13-max-assistant-design.md` (viết tắt "spec"). Kế hoạch B (Phase 2-4): `docs/superpowers/plans/2026-11-13-max-plan-b-phase-2-4.md`.

## Global Constraints

- Module Go tên `max` cho tới khi chủ nhân chốt đường dẫn (spec D10); đổi sau bằng `go mod edit -module` + sửa import.
- `CGO_ENABLED=0`. Không thêm dependency Go trực tiếp ngoài danh sách ở Tech Stack mà không ghi lý do trong commit.
- Trần dòng: Go viết tay <= 10.500 (CI cảnh báo), **mỗi file Go <= 400 dòng (CI ép)**; Svelte/TS <= 6.000 (trong đó `web/src/lib/ui` <= 1.200), **mỗi file <= 300 dòng (CI ép)**. Không tính thư mục code sinh (`gen/`, `db/`, `schema.d.ts`).
- Luật phụ thuộc (ép bằng `depguard`): `internal/domain` không import gói nội bộ nào; `internal/app/...` chỉ import `domain` và `platform`; `internal/adapter/x` import `app`, `domain`, `platform`, không import adapter khác; chỉ `cmd/max` nối dây.
- Không `panic` trên đường production. Lỗi bọc `%w`. Mọi hàm I/O nhận `context.Context` làm tham số đầu.
- SQL chỉ nằm trong `internal/adapter/sqlite`. Giao dịch do use case ở `app` mở. **Không mở giao dịch trong lúc tiến trình CLI chạy.**
- ID là UUID v7 dạng chuỗi, sinh ở `app`. Thời gian lưu epoch ms kiểu INTEGER. SQLite: WAL, `synchronous=NORMAL`, `foreign_keys=ON`, `busy_timeout=5000`, đúng một kết nối ghi, mọi giao dịch ghi `BEGIN IMMEDIATE`.
- Engine chính là `agy`. Tiến trình CLI luôn chạy với `HOME = <MAX_DATA_DIR>/agent-home/<engine>` (kèm các biến môi trường thư mục người dùng đa nền tảng), không bao giờ dùng HOME hay OS keyring của người dùng; riêng `agy` bắt buộc chạy dưới user hệ điều hành riêng (không có D-Bus keyring chung) hoặc trong container. Cấm truyền `--dangerously-skip-permissions` (trừ chế độ print trong vùng cô lập theo spec 8.4.5).
- Trần vận hành lấy nguyên từ spec: 40 lượt/run; 400 lượt/task; 20 phút chạy thật/run; 15 phút không có byte trên stdout thì hủy; hộp thư 200; `attempts` tối đa 3; cooldown 30 giây; 12 run/giờ/agent; `interactive` miễn cooldown và trần giờ; việc nền dừng ở 70% cửa sổ ngắn, `maintenance` ở 50%; cửa sổ ngắn 5 giờ; duyệt hết hạn sau 24 giờ; hook trả lời trong 30 giây.
- Design system (spec 8.13.1): không cài thư viện component nào. Nguyên tố giao diện chỉ nằm trong `web/src/lib/ui`; route và `lib/components` chỉ ráp từ đó. Icon chỉ import từ `@lucide/svelte`. Không file nào ngoài `web/src/app.css` chứa mã màu viết cứng. Mẫu hình ảnh phải khớp: `docs/superpowers/design/design-system.html`; token chép từ `docs/superpowers/design/tokens.css`.
- UI: chỉ runes (`$state`, `$derived`, `$effect`, `$props`, `{@render}`); cấm `svelte/store`, `$:`, `export let`, `<slot />`. Trước khi viết bất kỳ file Svelte nào: tra tài liệu qua Svelte MCP (`list-sections` rồi `get-documentation`), viết xong chạy `svelte-autofixer`. Dùng bun, không npm/pnpm. Type API chỉ lấy từ `web/src/lib/api/schema.d.ts` sinh ra.
- Commit: `<type>(<scope>): <mô tả> [TaskID: <mã task>]`. Thông điệp commit không chứa tên model hay nhà cung cấp AI, không có trailer `Co-Authored-By`.
- Cổng trước mỗi commit: `make check` (= `gofmt -l` rỗng, `go vet ./...`, `golangci-lint run`, `go test ./...`, kiểm trần dòng). Task có sửa `web/`: thêm `bun run check && bun run lint && bun run test`.
- Khi làm việc trong workspace Go: theo quy trình gopls MCP của chủ dự án (đọc trước, `go_symbol_references` trước khi đổi định nghĩa, `go_diagnostics` sau mỗi lần sửa, `go_vulncheck` khi đổi `go.mod`).

## Review Focus

Năm tình huống spec ngụ ý mà test của từng task dễ bỏ sót; mỗi dòng đã được gắn test vào task sở hữu (ghi trong ngoặc).

1. Đường dẫn cố thoát khỏi phạm vi: `..`, symlink trỏ ra ngoài vault, đường dẫn tuyệt đối, khác biệt chuẩn hóa Unicode (NFC/NFD) trong tên tiếng Việt. Kỳ vọng: bị từ chối, không ghi gì. (P1.05, P1.15)
2. CLI phát một dòng JSON dài nhiều MB, hoặc bị giết giữa một dòng dở. Kỳ vọng: không tràn bộ nhớ, dòng dở bị bỏ, run kết thúc `interrupted`. (P1.10)
3. `AGENT.md` hỏng: frontmatter sai YAML, thiếu trường, không phải UTF-8, hoặc dài quá trần. Kỳ vọng: giữ cấu hình cũ, tạo mục Inbox, không dừng các agent khác. (P1.17)
4. Đồng hồ nhảy (đổi giờ hệ thống, máy ngủ rồi thức): cửa sổ hạn mức và wakeup hẹn giờ. Kỳ vọng: không cửa sổ âm, không wakeup bị bỏ, không chạy bù hàng loạt. (P1.07, P1.20)
5. Chủ nhân gửi nhiều tin liên tiếp khi agent đang bận, hoặc bấm duyệt hai lần. Kỳ vọng: không tạo run đôi, tin được gộp vào một lượt, quyết định duyệt áp một lần. (P1.08, P1.14)

## Quy ước của kế hoạch

Mỗi task theo cùng một chu trình; kế hoạch chỉ ghi phần riêng của từng task:

1. **Test:** viết đúng các test được nêu (tên + khẳng định).
2. **Chạy thấy fail:** `go test ./<gói của task> -run '<TênTest>'`, kỳ vọng FAIL vì ký hiệu chưa tồn tại hoặc khẳng định sai.
3. **Hiện thực:** đúng chữ ký ở mục Interfaces; phần thân do người làm viết.
4. **Chạy pass + commit:** chạy lại test, rồi `make check`, rồi commit với thông điệp được nêu.

"Test bảng" nghĩa là một test Go duyệt qua một slice các trường hợp; kế hoạch liệt kê các hàng bắt buộc, người làm được thêm hàng.

---

## Hợp đồng dùng chung (định nghĩa ở P1.03 và P1.10; mọi task sau dùng đúng các tên này)

```go
// internal/domain/types.go
type ID = string                                   // UUID v7
type EngineName string                             // "agy" | "claude" | "codex"
const ( EngineAgy EngineName = "agy"; EngineClaude EngineName = "claude"; EngineCodex EngineName = "codex" )
func EngineMask(names []EngineName) int            // claude=1, codex=2, agy=4

type Priority int
const ( PrioInteractive Priority = 100; PrioTask Priority = 50; PrioRoutine Priority = 30; PrioMaintenance Priority = 10 )

type WakeKind string   // message | task_assigned | task_update | child_done | owner_reply
                       // | approval_result | routine | heartbeat | continue | system
func (k WakeKind) Coalescible() bool               // true: message, task_update, heartbeat, routine

type RunStatus string  // running | succeeded | failed | cancelled | timed_out | turn_limit
                       // | parked | quota_stopped | interrupted
func (s RunStatus) Terminal() bool                 // mọi giá trị trừ running

type Autonomy string   // suggest | auto
type ToolClass string  // R | C | Wd | Wt | P | Ba | E | X | Unknown
type Verdict int       // Allow | Deny | Ask
type Decision struct { Verdict Verdict; Reason string }
type RootOrigin string // owner | routine | agent | external
type BrowserProfile string // none | anon | owner
```

```go
// internal/app/ports.go
type Tx interface{ /* các truy vấn ghi, do adapter/sqlite hiện thực */ }
type Store interface {
    WithTx(ctx context.Context, fn func(tx Tx) error) error   // BEGIN IMMEDIATE ... COMMIT
    Read() Reader                                             // truy vấn đọc, không giao dịch
}
type Clock interface{ Now() time.Time }
type IDGen interface{ New() domain.ID }

type EngineCaps struct { PreToolGate, ToolAllowlist, Sandbox, PerRunMCP, Resume, StructuredOut bool }
type RunSpec struct {
    RunID, AgentID domain.ID
    AgentSlug      string
    Cwd            string
    AddDirs        []string
    SystemPrompt   string
    Prompt         string
    Model          string
    SessionID      string           // rỗng = phiên mới
    RunToken       string
    Autonomy       domain.Autonomy
    JSONSchema     string           // rỗng trừ lượt học
}
type EngineEvent struct {
    Type      string   // session | text_delta | tool_call | tool_result | usage | limit | final | error
    SessionID string
    Text      string
    ToolUseID, ToolName, InputPreview string
    ToolOK    bool
    InputTokens, OutputTokens int64
    LimitResetAt *time.Time
    ErrKind, ErrMessage string
}
type ToolAsk struct { RunToken, ToolName string; Input json.RawMessage; Path, SearchRoot string }
type PermissionFunc func(ctx context.Context, ask ToolAsk) domain.Decision
type Engine interface {
    Name() domain.EngineName
    Caps() EngineCaps
    Run(ctx context.Context, spec RunSpec, perm PermissionFunc) (<-chan EngineEvent, error)
    Health(ctx context.Context) EngineHealth      // {Installed, LoggedIn bool; Version string; ForeignMCP []string}
}
```

---

# PHASE 0 - SPIKE

Mã nguồn spike nằm trong `spikes/<mã>/` và **không được import từ mã production**; nó bị xóa khi Phase 1 xong. Mỗi spike kết thúc bằng một file kết quả `docs/spikes/<mã>.md` theo đúng khuôn:

```markdown
# <mã>: <tên>
Ngày: ...   Phiên bản công cụ: ...
## Kết quả từng mục     (bảng: mục | đạt/không | bằng chứng: lệnh đã chạy + đầu ra rút gọn)
## Số đo                (RAM, thời gian, token)
## Quyết định           (chọn nhánh nào của spec, giá trị cấu hình nào)
## Việc phải sửa trong spec/kế hoạch
```

Thứ tự: S0c trước tiên (cổng quyết định), rồi S4, S3, S1, S2, S5. S0c và S5 có tốn hạn mức gói Google của chủ nhân; phải được chủ nhân đồng ý trước khi chạy.

### S0c: Điều khiển `agy` qua ACP (cổng quyết định)

**Files:** Create `spikes/s0c/main.go`, `spikes/s0c/mcpping/main.go` (MCP server stdio một tool `ping`), `docs/spikes/S0c.md`

- [ ] **Bước 1:** Tạo thư mục `spikes/s0c/home`, đăng nhập `agy` với `HOME` trỏ vào đó. Kiểm: `HOME=spikes/s0c/home agy mcp list` trả danh sách rỗng. (Mục 1 của spec 8.4.4.)
- [ ] **Bước 2:** Viết client ACP tối thiểu trong `main.go`: sinh `agy acp`, gửi `initialize`, rồi `session/new` với `cwd` là thư mục tạm và `mcpServers` chứa `mcpping` kèm biến môi trường `MAX_RUN_TOKEN=probe-1`. Ghi lại toàn bộ thông điệp hai chiều vào `spikes/s0c/trace.ndjson`.
- [ ] **Bước 3:** Gửi `session/prompt` "Gọi tool ping rồi trả lời kết quả". Đạt khi `mcpping` ghi nhận lời gọi và thấy `MAX_RUN_TOKEN=probe-1`. (Mục 2.)
- [ ] **Bước 4:** Gửi prompt yêu cầu ghi file `out.txt` và chạy lệnh `echo hi`. Với mỗi `session/request_permission` nhận được: lần chạy thứ nhất trả `allow`, lần chạy thứ hai trả `deny`. Đạt khi ở lần `deny` file không được tạo và lệnh không chạy. Ghi lại loại tool nào không hỏi. (Mục 3.)
- [ ] **Bước 5:** Thoát tiến trình, sinh lại, `session/load` (nếu lỗi thì `session/resume`) với id cũ, hỏi "Tôi vừa nhờ bạn làm gì?". Đạt khi câu trả lời nhắc đúng việc cũ. (Mục 4.)
- [ ] **Bước 6:** Tạo phiên mới có nối thêm system prompt "Luôn kết thúc câu trả lời bằng từ MAXOK" qua năng lực `systemPrompt.append`. Đạt khi câu trả lời kết thúc bằng `MAXOK`. (Mục 5.)
- [ ] **Bước 7:** Chạy 3 tiến trình `agy acp` song song trên cùng `home`, mỗi cái một prompt khác nhau gọi `ping`. Đạt khi cả 3 xong, mỗi `mcpping` thấy đúng token của mình. (Mục 6.)
- [ ] **Bước 8:** Trích từ `trace.ndjson`: trường nào mang số token mỗi lượt; thông điệp nào xuất hiện khi gói báo hết hạn mức (nếu gặp). Đo RSS mỗi tiến trình bằng `ps -o rss=` và thời gian từ sinh tiến trình tới `initialize` xong. (Mục 7, 8.)
- [ ] **Bước 9:** Nếu bước 3 hoặc 4 không đạt: lặp lại phép đo tương đương với `agy -p --output-format stream-json --input-format stream-json`, MCP khai ở mức HOME bằng `agy mcp add ping -- <đường dẫn mcpping>`, và kiểm `mcpping` có nằm dưới cây tiến trình của `agy` không (`ps -o ppid=`). (Mục 9.)
- [ ] **Bước 10:** Viết `docs/spikes/S0c.md`. Mục Quyết định ghi rõ một trong ba: **A** (ACP, `agy` mức 1), **B** (print, `agy` mức 3), **Dừng** (tool của Max không nối được ổn định). Ghi tên trường usage, mẫu nhận diện hết hạn mức, RSS, và danh sách tool không hỏi quyền.
- [ ] **Bước 11:** Commit `chore(spike): S0c agy ACP findings [TaskID: S0c]`.

**Nếu quyết định là Dừng:** không làm Phase 1; báo chủ nhân với bằng chứng.

### S4: Mô hình actor trên SQLite

**Files:** Create `spikes/s4/main.go`, `spikes/s4/schema.sql` (chép các bảng `teams`, `agents`, `conversations`, `wakeups`, `runs` từ spec mục 9), `docs/spikes/S4.md`

- [ ] **Bước 1:** Chèn 5.000 agent (10 team) và 100.000 wakeup ngẫu nhiên theo 4 mức ưu tiên.
- [ ] **Bước 2:** Vòng nhận việc theo spec 8.2.2 với "engine giả" là goroutine ngủ 5-50 ms, 8 chỗ. Chạy tới khi hết wakeup.
- [ ] **Bước 3:** Sau mỗi 1.000 run chạy các truy vấn kiểm bất biến: không agent nào có hơn 1 run `running`; không wakeup `queued` nào thuộc agent có `ready = 0`; `teams.running` khớp số run đang chạy.
- [ ] **Bước 4:** Đo thời gian giao dịch nhận việc (p50, p95) ở Q = 1.000, 100.000, 1.000.000. Đạt khi p95 < 20 ms và không tăng quá 2 lần giữa Q nhỏ nhất và lớn nhất.
- [ ] **Bước 5:** Giết tiến trình ngẫu nhiên 20 lần giữa chừng, khởi động lại với bước khôi phục của spec 8.2.5. Đạt khi sau cùng mọi wakeup ở `done`, 0 vi phạm.
- [ ] **Bước 6:** Viết `docs/spikes/S4.md` (kèm chỉ mục nào thật sự được dùng, theo `EXPLAIN QUERY PLAN`). Commit `chore(spike): S4 actor model on sqlite [TaskID: S4]`.

### S3: SvelteKit nhúng vào Go, SSE

**Files:** Create `spikes/s3/web/` (SvelteKit tối thiểu), `spikes/s3/main.go`, `docs/spikes/S3.md`

- [ ] **Bước 1:** Tra tài liệu SvelteKit 3 + adapter-static qua Svelte MCP (cấu hình SPA, fallback). Ghi các mục đã đọc vào file kết quả.
- [ ] **Bước 2:** Dựng app 2 route (`/`, `/activity`) với `ssr = false`, build bằng bun, nhúng `build/` vào Go bằng `embed.FS`, phục vụ `index.html` cho mọi đường dẫn không bắt đầu bằng `/api`.
- [ ] **Bước 3:** Endpoint `/api/v1/events` phát 1 sự kiện/giây; trang `/` hiển thị bằng một `EventSource` trong module `events.svelte.ts` dùng `$state`.
- [ ] **Bước 4:** Đạt khi: tải lại trực tiếp `/activity` trả về app; sự kiện tới UI < 200 ms trên loopback; `svelte-autofixer` không báo lỗi. Nếu có Tailscale: lặp lại qua Tailscale Serve và ghi độ trễ.
- [ ] **Bước 4b:** Chép `docs/superpowers/design/tokens.css` vào `src/app.css` sau `@import "tailwindcss"`. Trên `/activity` dựng một nút dùng class `bg-primary text-primary-foreground`, một icon import từ `@lucide/svelte`, một `<dialog>` mở bằng `showModal()` và một menu dùng thuộc tính `popover`. Đạt khi: class token sinh đúng màu `#181818`; build chỉ chứa icon đã import (ghi kích thước JS gzip trước và sau khi thêm icon); Esc đóng được hộp thoại và menu trên Chrome, Firefox, Safari bản hiện hành. Nếu `popover` không đạt trên một trình duyệt: ghi phương án thay (menu tự định vị bằng `getBoundingClientRect`) vào file kết quả.
- [ ] **Bước 5:** Viết `docs/spikes/S3.md` (cấu hình adapter đã dùng, kích thước JS gzip, kết quả bước 4b). Commit `chore(spike): S3 sveltekit embed and sse [TaskID: S3]`.

### S1: Trình duyệt CloakBrowser qua Playwright MCP

**Files:** Create `spikes/s1/main.go`, `docs/spikes/S1.md`. Cần `CLOAKBROWSER_LICENSE_KEY` của chủ nhân.

- [ ] **Bước 1:** Xác định `cloakserve` có dùng được ngoài image Docker không (`pip show cloakbrowser`, tìm lệnh `cloakserve`). Ghi kết quả.
- [ ] **Bước 2:** Chạy `cloakserve --data-dir=<dir> --keep-profiles`; lấy `webSocketDebuggerUrl` từ `/json/version?fingerprint=anon`; chạy `@playwright/mcp@0.0.85 --cdp-endpoint <url>`.
- [ ] **Bước 3:** Từ Go (client MCP của go-sdk) gọi: liệt kê tool; điều hướng tới 3 trang công khai; lấy snapshot; click một link theo ref.
- [ ] **Bước 4:** Khởi động lại `cloakserve`; kiểm một cookie đặt ở bước 3 còn tồn tại.
- [ ] **Bước 5:** Đo RSS của Chromium với 1 và 2 tab. Lập bảng mọi tên tool và xếp loại đọc/thao tác.
- [ ] **Bước 6:** Viết `docs/spikes/S1.md`: cách chạy được chọn (tiến trình con hay container riêng), allowlist tool chỉ đọc, RSS. Commit `chore(spike): S1 cloakbrowser via playwright mcp [TaskID: S1]`.

### S2: agentmemory với nhiều agent

**Files:** Create `spikes/s2/main.go`, `docs/spikes/S2.md`

- [ ] **Bước 1:** Chạy `@agentmemory/agentmemory@0.9.33` với `--data-dir` tạm, `AGENTMEMORY_TOOLS=core`, không cấu hình LLM.
- [ ] **Bước 2:** Ghi 200.000 ký ức qua `POST /agentmemory/remember`, rải trên 10.000 `agentId`.
- [ ] **Bước 3:** Chạy 1.000 lượt `POST /agentmemory/smart-search` có `agentId`, `limit: 5`. Đạt khi p95 < 500 ms.
- [ ] **Bước 4:** Ghi RSS của tiến trình trước/sau; khởi động lại; kiểm một ký ức mẫu còn tìm được.
- [ ] **Bước 5:** Viết `docs/spikes/S2.md`: đạt hay phải dùng phương án SQLite FTS5 sau port `EpisodicMemory`. Commit `chore(spike): S2 agentmemory multi-agent load [TaskID: S2]`.

### S5: Đo hạn mức thật của gói Google

**Files:** Create `spikes/s5/main.go`, `docs/spikes/S5.md`. Tốn hạn mức gói.

- [ ] **Bước 1:** Dùng client của S0c chạy lặp một việc mẫu cố định (đọc 3 file, viết 1 file tóm tắt ~300 từ), 1 tiến trình, ghi token và thời gian mỗi lượt.
- [ ] **Bước 2:** Chạy tới khi gói báo hết hạn mức hoặc đủ 4 giờ. Ghi: số lượt, tổng token, thông điệp báo hết, giờ mở lại nếu có.
- [ ] **Bước 3:** Lặp lại với 3 tiến trình song song trong 30 phút để xem tốc độ tiêu hạn mức có tuyến tính không.
- [ ] **Bước 4:** Viết `docs/spikes/S5.md`: đề xuất `MAX_SLOTS_AGY`, `MAX_QUOTA_SHORT_TOKENS`, lịch heartbeat mặc định. Commit `chore(spike): S5 measured plan quota [TaskID: S5]`.

**Cổng Phase 0:** S0c quyết định A hoặc B; S4 đạt. S1, S2, S3, S5 hoặc đạt hoặc đã chọn phương án dự phòng trong file kết quả.

---

# PHASE 1 - LÕI MỘT AGENT

## Bản đồ file của Phase 1

| File | Trách nhiệm |
|---|---|
| `cmd/max/main.go` | Phân lệnh con, nối dây, vòng đời |
| `internal/platform/{config,logging,redact}.go` | Cấu hình từ env, log JSON, che secret |
| `internal/domain/{types,pathscope,policy,quota,turnlimit}.go` | Kiểu và luật thuần |
| `internal/app/ports.go` | Port |
| `internal/app/actor/{wake,claim,recover}.go` | Hộp thư, nhận việc, khôi phục |
| `internal/app/run/{execute,finalize}.go` | Vòng đời run |
| `internal/app/approvals/service.go` | Duyệt: dừng và tiếp tục |
| `internal/app/limits/{slots,quota}.go` | Chỗ chạy, sổ hạn mức |
| `internal/app/prompt/build.go` | Ghép system prompt và message |
| `internal/app/agentsync/sync.go` | `AGENT.md` -> bảng `agents` |
| `internal/app/chat/service.go` | Thread, message |
| `internal/app/scheduler/tick.go` | Việc định kỳ nội bộ |
| `internal/app/dispatch/loop.go` | Vòng điều phối: có chỗ thì nhận việc và chạy |
| `internal/app/tools/core.go` | Logic các tool của Max (không phụ thuộc MCP) |
| `internal/adapter/sqlite/` | Migration, truy vấn `sqlc`, `Store` |
| `internal/adapter/engine/{proc.go,agy/}` | Sinh tiến trình; driver `agy` |
| `internal/adapter/mcpserver/` | Tool của Max + lệnh `max mcp` |
| `internal/adapter/scopeaudit/audit.go` | Kiểm và hoàn tác ghi sai phạm vi |
| `internal/adapter/vaultgit/git.go` | Git, cài vault mẫu |
| `internal/adapter/httpapi/` | REST, SSE, auth, UI nhúng |
| `internal/testutil/{fakecli,fakeengine,testdb}/` | Công cụ test |
| `api/openapi.yaml` | Hợp đồng API |
| `web/` | SPA Svelte 5 |

### P1.01: Khung repo và cổng chất lượng

**Files:** Create `go.mod`, `Makefile`, `.golangci.yml`, `.gitignore`, `scripts/linecount.sh`, `cmd/max/main.go`, `internal/platform/doc.go`

**Interfaces:** Produces: lệnh `make check`, `make build`; lệnh con `max version`.

- [ ] **Test:** `scripts/linecount_test.sh`: tạo file Go giả 401 dòng trong thư mục tạm, kỳ vọng `scripts/linecount.sh <dir>` thoát mã 1 và in tên file; file 400 dòng thoát mã 0.
- [ ] **Chạy thấy fail:** `bash scripts/linecount_test.sh`.
- [ ] **Hiện thực:** `git init`; `go mod init max`; `Makefile` với đích `check` như Global Constraints; `.golangci.yml` bật `depguard` với 3 luật phụ thuộc; `linecount.sh` bỏ qua `gen/`, `db/`, `*_test.go` khi tính trần tổng nhưng vẫn kiểm trần mỗi file; `main.go` in phiên bản.
- [ ] **Chạy pass + commit:** `make check`; `chore(repo): scaffold module, lint rules and line caps [TaskID: P1.01]`.

### P1.02: Cấu hình, log, che secret

**Files:** Create `internal/platform/config.go`, `logging.go`, `redact.go` + test tương ứng

**Interfaces:** Produces:
```go
type Config struct {
    DataDir, VaultDir, Listen, PublicURL, TZ string
    Engines      []domain.EngineName          // rỗng = tự phát hiện
    Slots        map[domain.EngineName]int    // mặc định 3 mỗi engine
    QuotaShortHours int                        // 5
    QuotaShortTokens, QuotaWeekTokens *int64   // nil = chế độ học
    QuotaBackgroundShare float64               // 0.7
    AdminPassword string
}
func Load(getenv func(string) string) (Config, error)
func Redact(s string) string
func NewLogger(w io.Writer, level string) *slog.Logger   // mọi giá trị chuỗi đi qua Redact
```
- [ ] **Test:** `TestLoadDefaults` (không biến nào: `Listen == "127.0.0.1:8787"`, `Slots[agy] == 3`, `QuotaBackgroundShare == 0.7`, `QuotaShortHours == 5`); `TestLoadRejectsBadSlots` (`MAX_SLOTS_AGY=0` và `=9` trả lỗi nêu tên biến; hợp lệ 1-8); `TestRedact` bảng: chuỗi chứa `sk-ant-abc123...`, token bot Telegram `123456:AA...`, `Bearer xyz`, `https://u:p@host/x`, khối `-----BEGIN PRIVATE KEY-----` đều không còn phần bí mật trong kết quả và vẫn giữ phần còn lại của dòng.
- [ ] **Chạy thấy fail.**
- [ ] **Hiện thực** theo chữ ký; tên biến môi trường lấy từ bảng cấu hình ở spec 8.15.
- [ ] **Chạy pass + commit:** `feat(platform): config, redacting logger [TaskID: P1.02]`.

### P1.03: Kiểu miền

**Files:** Create `internal/domain/types.go`, `types_test.go`

**Interfaces:** Produces: toàn bộ khối `internal/domain/types.go` ở mục Hợp đồng dùng chung.

- [ ] **Test:** `TestEngineMask` (`[agy] -> 4`, `[claude, codex] -> 3`, `[agy, claude] -> 5`, rỗng -> 0); `TestCoalescible` (true cho `message`, `task_update`, `heartbeat`, `routine`; false cho `task_assigned`, `child_done`, `approval_result`, `owner_reply`, `continue`); `TestTerminal` (`running` false, mọi trạng thái khác true).
- [ ] **Chạy thấy fail.** **Hiện thực.**
- [ ] **Chạy pass + commit:** `feat(domain): core types [TaskID: P1.03]`.

### P1.04: SQLite store và migration

**Files:** Create `internal/adapter/sqlite/migrations/001_init.sql`, `query/*.sql`, `sqlc.yaml`, `store.go`, `store_test.go`; `internal/testutil/testdb/testdb.go`; `internal/app/ports.go` (phần `Store`, `Tx`, `Reader`, `Clock`, `IDGen`)

**Interfaces:** Produces: `sqlite.Open(ctx, path string) (*Store, error)`; `(*Store).WithTx`; `(*Store).Read`; `testdb.New(t) app.Store` (file tạm, đã migrate).

- [ ] **Test:** `TestMigrateCreatesSchema` (sau `Open`, mọi bảng ở spec mục 9 tồn tại; `PRAGMA journal_mode` = `wal`; `foreign_keys` = 1); `TestWithTxRollsBackOnError` (hàm trả lỗi thì dòng đã chèn không còn); `TestSingleWriter` (hai `WithTx` đồng thời từ 2 goroutine đều xong, không lỗi `SQLITE_BUSY`); `TestUniqueIdempotency` (hai wakeup `queued` cùng `idempotency_key` thì lần hai lỗi ràng buộc).
- [ ] **Chạy thấy fail.**
- [ ] **Hiện thực:** `001_init.sql` chép nguyên DDL ở spec mục 9, cộng các bảng "còn lại" (`threads`, `messages`, `task_events`, `run_events`, `inbox_items`, `proposals`, `routine_state`, `connections`, `users`, `auth_sessions`, `kv`) với cột theo bảng mô tả trong spec. Một `*sql.DB` ghi với `SetMaxOpenConns(1)`, một `*sql.DB` đọc. `goose` chạy migration nhúng lúc `Open`.
- [ ] **Chạy pass + commit:** `feat(sqlite): schema, store, single-writer transactions [TaskID: P1.04]`.

### P1.05: Phạm vi đường dẫn vault

**Files:** Create `internal/domain/pathscope.go`, `pathscope_test.go`

**Interfaces:** Produces:
```go
type Zone int   // ZoneTrusted | ZoneData | ZoneOutside
type AgentScope struct { Slug, TeamSlug string; ReadExtra []string }
func CleanRel(p string) (rel string, ok bool)             // từ chối tuyệt đối, "..", byte NUL; chuẩn hóa NFC
func Classify(rel string) Zone
func CanRead(a AgentScope, rel string) bool
func CanWrite(a AgentScope, rel string) (ok bool, reason string)
```
- [ ] **Test (bảng `TestCanWrite`, agent `slug=writer-1`, `team=marketing`):**

| Đường dẫn | Kỳ vọng |
|---|---|
| `agents/writer-1/notes/a.md` | được |
| `agents/writer-1/artifacts/x/y.md` | được |
| `agents/writer-1/AGENT.md` | không (vùng tin cậy) |
| `agents/other/notes/a.md` | không |
| `wiki/x.md`, `sources/x.md` | được |
| `memory/facts/x.md`, `company/COMPANY.md`, `teams/marketing/TEAM.md`, `skills/a/SKILL.md`, `routines/r.md`, `.claude/settings.json`, `CLAUDE.md` | không |
| `skills/_proposed/a/SKILL.md` | được |
| `../etc/passwd`, `/etc/passwd`, `agents/writer-1/notes/../../other/x` | không (`CleanRel` trả `ok=false`) |

`TestCanRead`: đọc được `company/`, `teams/marketing/`, `wiki/`, `agents/writer-1/`; không đọc được `agents/other/notes/a.md`, `teams/sales/TEAM.md`. `TestCleanRelUnicode`: `wiki/Tiếng.md` (NFD) và `wiki/Tiếng.md` (NFC) cho cùng kết quả.
- [ ] **Chạy thấy fail.** **Hiện thực** (thuần chuỗi; không chạm hệ thống file; việc giải symlink thuộc P1.15).
- [ ] **Chạy pass + commit:** `feat(domain): vault path scope rules [TaskID: P1.05]`.

### P1.06: Policy

**Files:** Create `internal/domain/policy.go`, `policy_test.go`

**Interfaces:** Consumes: P1.03, P1.05. Produces:
```go
type AgentPolicy struct {
    Scope       AgentScope
    Autonomy    Autonomy
    Web         bool
    Browser     BrowserProfile
    Shell       bool
    EngineLevel int            // 1, 2 hoặc 3 theo spec 8.4.5
}
type ToolCall struct {
    Name       string
    Class      ToolClass
    Rel        string          // đường dẫn vault đã CleanRel, nếu có
    SearchRoot string          // cho Grep/Glob
    Connection string
    InputHash  string
}
type Grant struct { Connection, Tool string; AllowedRootOrigins []RootOrigin; ExpiresAt *time.Time; Revoked bool }
func ClassifyBuiltin(tool string) ToolClass     // Read,Grep,Glob -> R; Write,Edit -> Wd hoặc Wt theo đường dẫn; Bash -> X
func Decide(a AgentPolicy, c ToolCall, origin RootOrigin, grants []Grant, now time.Time) Decision
```
- [ ] **Test (bảng `TestDecide`):** mọi ô của ma trận ở spec 8.6 cho cả `suggest` và `auto`, tối thiểu các hàng: `R` trong phạm vi -> Allow (cả hai); `R` ngoài phạm vi -> Deny; `Wd` -> Deny với `suggest`, Allow với `auto`; `Wt` -> Deny (cả hai); `C` tool `ask_owner` -> Allow với `suggest`; `C` tool `delegate` -> Deny với `suggest`, Allow với `auto`; `E` không grant -> Ask; `E` có grant khớp và `origin=owner` -> Allow; cùng grant nhưng `origin=external` -> Ask; grant đã `Revoked` hoặc hết hạn -> Ask; `X` -> Deny khi `Shell=false`; `Unknown` -> Deny với `suggest`, Ask với `auto`; `Ba` với `Browser=none` -> Deny. Mỗi `Decision` không phải Allow phải có `Reason` khác rỗng.
- [ ] **Chạy thấy fail.** **Hiện thực** (hàm thuần).
- [ ] **Chạy pass + commit:** `feat(domain): tool permission policy [TaskID: P1.06]`.

### P1.07: Luật hạn mức

**Files:** Create `internal/domain/quota.go`, `quota_test.go`

**Interfaces:** Produces:
```go
type QuotaView struct {
    UsedShort, UsedWeek int64
    EstShort, EstWeek   *int64       // nil = chưa biết trần
    LimitedUntil        *time.Time
}
type Admission int   // Admit | DeferShare | DeferLimited
func Admit(v QuotaView, p Priority, bgShare float64, now time.Time) Admission
func LearnLimit(v QuotaView, resetAt *time.Time, now time.Time) QuotaView   // đặt EstShort = UsedShort; LimitedUntil
func WindowStart(now time.Time, hours int) time.Time                          // cắt xuống bội số của cửa sổ, UTC
func NextProbe(attempt int) time.Duration                                      // 15, 30, 60, 60... phút
```
- [ ] **Test:** `TestAdmit` bảng: trần chưa biết -> Admit mọi lớp; `LimitedUntil` ở tương lai -> DeferLimited mọi lớp; dùng 70% với `bgShare=0.7` -> `PrioTask`, `PrioRoutine` DeferShare, `PrioInteractive` Admit; dùng 50% -> `PrioMaintenance` DeferShare, `PrioRoutine` Admit; dùng 100% -> `PrioInteractive` vẫn Admit (chỉ gói mới chặn được lớp này). `TestLearnLimit`: `UsedShort=800` -> `EstShort=800`; `resetAt=nil` -> `LimitedUntil = now + NextProbe(0)`. `TestWindowStartMonotonic`: với `now` lùi 2 giờ (đồng hồ nhảy lùi) kết quả không lớn hơn kết quả trước đó; hai thời điểm trong cùng cửa sổ cho cùng giá trị.
- [ ] **Chạy thấy fail.** **Hiện thực.**
- [ ] **Chạy pass + commit:** `feat(domain): subscription quota admission rules [TaskID: P1.07]`.

### P1.08: Actor - `Wake` và trạng thái sẵn sàng

**Files:** Create `internal/app/actor/wake.go`, `wake_test.go`; thêm truy vấn vào `internal/adapter/sqlite/query/actor.sql`

**Interfaces:** Consumes: `app.Store`, `app.Tx`, `Clock`, `IDGen`. Produces:
```go
type WakeInput struct {
    AgentID        domain.ID
    Kind           domain.WakeKind
    Priority       domain.Priority
    ConversationID *domain.ID
    Payload        json.RawMessage     // chỉ tham chiếu
    IdempotencyKey *string
    NotBefore      time.Time
    CauseRunID     *domain.ID
    Hops           int
}
var ErrMailboxFull = errors.New("mailbox full")
func (s *Service) Wake(ctx context.Context, tx app.Tx, in WakeInput) (id domain.ID, coalesced bool, err error)
func (s *Service) RecomputeReady(ctx context.Context, tx app.Tx, agentID domain.ID, now time.Time) error
```
- [ ] **Test (SQLite thật qua `testdb`):** `TestWakeSetsReady` (sau `Wake`, `agents.ready=1`, `ready_priority` = ưu tiên, `ready_since` = `created_at`); `TestWakeCoalescesMessages` (3 `message` cùng conversation -> 1 dòng `queued`, `coalesced_count=2`, trả `coalesced=true` ở lần 2 và 3); `TestWakeNeverCoalescesAssignments` (3 `task_assigned` cùng conversation -> 3 dòng); `TestWakeIdempotent` (cùng `IdempotencyKey` hai lần -> 1 dòng, lần hai `coalesced=true`, không lỗi); `TestWakeFutureNotReady` (`NotBefore` = now + 1 giờ -> `ready=0`; `RecomputeReady` với now + 2 giờ -> `ready=1`); `TestMailboxFull` (đã có 200 wakeup `queued` không gộp được -> lần `message` thứ 201 trả `ErrMailboxFull`; `task_assigned` thứ 201 vẫn được chèn); `TestBurstOfOwnerMessages` (50 `message` cùng thread khi agent đang có `active_run_id`: sau khi run kết thúc chỉ có 1 wakeup chờ, không tạo run thứ hai).
- [ ] **Chạy thấy fail.** **Hiện thực** theo spec 8.2.1 và 8.2.3.
- [ ] **Chạy pass + commit:** `feat(actor): durable wake with coalescing and ready state [TaskID: P1.08]`.

### P1.09: Actor - nhận việc, rút hộp thư, khôi phục

**Files:** Create `internal/app/actor/claim.go`, `recover.go` + test

**Interfaces:** Consumes: P1.07, P1.08. Produces:
```go
type Claimed struct {
    RunID, AgentID, ConversationID domain.ID
    TaskID        *domain.ID
    Engine        domain.EngineName
    Priority      domain.Priority
    Wakeups       []Wakeup            // id, kind, payload
    SessionID     string              // rỗng nếu phiên mới trên engine này
    EngineChanged bool                // phải kèm <history>
}
type Capacity struct { Free map[domain.EngineName]int; Admit func(domain.EngineName, domain.Priority) domain.Admission }
func (s *Service) ClaimBatch(ctx context.Context, cap Capacity, now time.Time) ([]Claimed, error)
func (s *Service) Recover(ctx context.Context, now time.Time) (interrupted int, err error)
```
- [ ] **Test:** `TestClaimOrder` (3 agent sẵn sàng với ưu tiên 100/50/50 và `ready_since` khác nhau, 3 chỗ: thứ tự trả về là ưu tiên cao trước, rồi chờ lâu trước); `TestClaimOneRunPerAgent` (agent đang có `active_run_id` không được trả về dù có wakeup); `TestClaimRespectsFreeSlots` (`Free[agy]=1`, 3 agent sẵn sàng -> trả 1); `TestClaimTeamCap` (team `max_running=2`, 5 agent cùng team sẵn sàng, 5 chỗ -> trả 2); `TestDrainSingleConversation` (agent có wakeup của 2 conversation: run chỉ nhận wakeup của conversation ưu tiên cao nhất, agent vẫn `ready=1`); `TestDrainCapsAt20`; `TestClaimDefersOnQuota` (`Admit` trả `DeferShare` cho `PrioTask`: agent không được nhận, wakeup vẫn `queued`); `TestCooldownSkippedForInteractive` (agent trong cooldown: wakeup `PrioTask` không được nhận, `PrioInteractive` được nhận); `TestEnginePick` (`engines=[agy, claude]`, `Free[agy]=0`, `Free[claude]=1` -> `Engine=claude`, `EngineChanged=true` nếu conversation đã có phiên `agy`); `TestRecover` (2 run `running` -> `interrupted`, wakeup `claimed` -> `queued`, `active_run_id` NULL, `teams.running=0`, trả 2).
- [ ] **Chạy thấy fail.** **Hiện thực** theo spec 8.2.2 và 8.2.5; mọi thứ trong một `WithTx`.
- [ ] **Chạy pass + commit:** `feat(actor): fair claim, mailbox drain, crash recovery [TaskID: P1.09]`.

### P1.10: Port engine, engine giả, và trình sinh tiến trình

**Files:** Create phần engine của `internal/app/ports.go`; `internal/testutil/fakeengine/fake.go`; `internal/adapter/engine/proc.go`, `proc_test.go`; `internal/testutil/fakecli/main.go`

**Interfaces:** Produces: khối `Engine`, `RunSpec`, `EngineEvent`, `PermissionFunc` ở Hợp đồng dùng chung;
```go
// fakeengine
func New(name domain.EngineName, script ...app.EngineEvent) *Fake     // phát đúng script rồi đóng kênh
func (f *Fake) OnTool(fn func(app.ToolAsk))                           // gọi perm cho mỗi tool_call trong script

// adapter/engine/proc.go
type Proc struct { PID, PGID int }
type ProcSpec struct { Bin string; Args, Env []string; Dir string; IdleTimeout, Grace time.Duration }
func Start(ctx context.Context, s ProcSpec) (*Proc, io.WriteCloser, <-chan []byte, <-chan error, error)   // stdin, từng dòng stdout, lỗi kết thúc
func KillGroup(pgid int, grace time.Duration) error
```
`fakecli` là binary test: đọc biến `FAKECLI_SCRIPT` (đường dẫn file các dòng sẽ in ra stdout, kèm lệnh `sleep <ms>`, `hang`, `exit <mã>`).
- [ ] **Test:** `TestStartStreamsLines` (script 3 dòng -> nhận đúng 3 dòng, kênh lỗi trả nil); `TestHugeLine` (một dòng 8 MB -> nhận nguyên vẹn, RSS của test không tăng quá 64 MB so với trước); `TestPartialLineOnKill` (script in nửa dòng rồi `hang`, hủy context -> không dòng nào được phát cho nửa dòng dở, kênh lỗi trả lỗi "killed"); `TestIdleTimeout` (script `hang` với `IdleTimeout=200ms` -> bị giết trong < 1 giây); `TestKillGroupKillsChildren` (script sinh tiến trình con `sleep 60`; sau `KillGroup` không còn tiến trình nào trong nhóm); `TestEnvIsExact` (tiến trình con chỉ thấy đúng các biến trong `Env`, không thừa biến của tiến trình cha).
- [ ] **Chạy thấy fail.** **Hiện thực:** `Setpgid: true`; không qua shell; đọc stdout bằng `bufio.Reader.ReadBytes('\n')`; SIGTERM cả nhóm, chờ `Grace`, rồi SIGKILL.
- [ ] **Chạy pass + commit:** `feat(engine): engine port, process runner, test doubles [TaskID: P1.10]`.

### P1.11: Vòng đời run

**Files:** Create `internal/app/run/execute.go`, `finalize.go`, `internal/domain/turnlimit.go` + test

**Interfaces:** Consumes: P1.09 (`Claimed`), P1.10 (`Engine`). Task này khai hai port trong `internal/app/ports.go` và dùng bản giả trong test; bản thật đến ở P1.12 và P1.16: `type PromptBuilder interface { System(ctx, agentID domain.ID) (string, error); RunMessage(ctx, c actor.Claimed) (string, error) }` và `type Vault` (chữ ký ở P1.16). Produces:
```go
type Outcome struct {
    Status       domain.RunStatus
    Turns        int
    InputTokens, OutputTokens int64
    SessionID    string
    Summary, Err string
    TouchedPaths []string
    LimitResetAt *time.Time
}
func (s *Service) Execute(ctx context.Context, c actor.Claimed) (Outcome, error)   // chặn tới khi run kết thúc
func (s *Service) Finalize(ctx context.Context, runID domain.ID, o Outcome) error  // một giao dịch
// domain
func NextStepAfter(status domain.RunStatus, taskTurns, idleRuns, attempts int) (requeue, continueWake, blockTask bool, reason string)
```
- [ ] **Test (engine giả):** `TestSucceeded` (script `session`, `text_delta`, `final` -> run `succeeded`, wakeup `done`, `active_run_id` NULL, `teams.running` giảm, `conversations.sessions` có id của engine); `TestTurnLimit` (script 41 lượt -> run bị hủy ở lượt 41, trạng thái `turn_limit`, có đúng 1 wakeup `continue` mới cùng conversation); `TestTaskTurnCapBlocks` (`conversations.turns_used=395` + run 6 lượt -> không tạo `continue`, task `blocked` với `blocked_reason='stalled'`, có mục Inbox); `TestInterruptedRequeues` (script lỗi giữa chừng -> `interrupted`, wakeup về `queued` với `attempts=1`); `TestThirdFailureDrops` (wakeup đã có `attempts=3` -> sau thất bại: `dropped`, task `blocked('error')`, có mục Inbox); `TestQuotaStopped` (script có sự kiện `limit` -> `quota_stopped`, wakeup về `queued`, `attempts` không tăng, `quota_state.limited_until` được đặt); `TestRunTokenInvalidAfterEnd` (sau `Finalize`, tra token trả "không còn hiệu lực"); `TestCheckoutCleared` (task có `checkout_run_id` = run -> NULL sau mọi kiểu kết thúc); `TestColdResumeStartsFreshSession` (run trước cách 61 phút và báo 60.001 token đầu vào -> `RunSpec.SessionID` rỗng, prompt có khối `<history>`, có sự kiện `session_rebuilt`; cách 59 phút hoặc 59.999 token -> resume phiên cũ); `TestFinalizeSurvivesStoreError` (store trả lỗi ở lần `Finalize` đầu -> `Execute` thử lại tối đa 3 lần với backoff; nếu vẫn lỗi, run vẫn ở `running` để `Recover` xử lý ở lần khởi động sau, không mất wakeup).
- [ ] **Chạy thấy fail.** **Hiện thực:** bảng "Sau mỗi kiểu kết thúc" ở spec 8.3 chuyển thành `NextStepAfter` (thuần, có test bảng riêng `TestNextStepAfter` đủ 6 hàng của bảng đó). Định nghĩa "lượt" (dùng thống nhất cho trần 40 và 400): một lượt bắt đầu ở sự kiện đầu tiên của run và mỗi lần engine phát `text_delta` hoặc `tool_call` ngay sau một `tool_result`. Bộ đếm nằm trong `Execute`, không phụ thuộc engine.
- [ ] **Chạy pass + commit:** `feat(run): run lifecycle with park, continue, retry and quota stop [TaskID: P1.11]`.

### P1.12: Ghép prompt

**Files:** Create `internal/app/prompt/build.go`, `build_test.go`; `prompts/kernel.md`

**Interfaces:** Produces:
```go
type SystemInput struct { Kernel, Company, Team, Role string; SkillIndex []SkillRef }
type RunInput struct { Now time.Time; TZ string; TaskSummary, GoalAncestry string; QuotaNote string; Notes string; Recall []string; Inbox []InboxItem; History string }
func BuildSystem(in SystemInput) (text string, warnings []string)
func BuildRunMessage(in RunInput) string
```
- [ ] **Test:** `TestSystemOrder` (thứ tự các khối: kernel, công ty, team, vai trò, chỉ mục skill); `TestSystemCaps` (công ty 151 dòng -> cắt còn 150 và có 1 cảnh báo nêu tên khối; team trần 80; vai trò trần 200; kernel > 6.000 ký tự -> lỗi cứng ở test `TestKernelFileUnderCap` đọc file thật `prompts/kernel.md`); `TestSystemHasNoClock` (kết quả không chứa năm hiện tại hay chuỗi giờ); `TestRunMessageBlocks` (có đủ `<context>`, `<notes>`, `<recall>`, `<inbox>` theo thứ tự; `Notes` cắt ở 80 dòng; `Recall` tối đa 5 mục; tổng <= 12.000 ký tự, vượt thì cắt `<recall>` trước rồi `<notes>`); `TestHistoryOnlyWhenEngineChanged` (`History` rỗng -> không có khối `<history>`); `TestOnlyMountedToolsMentioned` (`SystemInput` nhận thêm `MountedTools []string`: agent không được cấp trình duyệt, hoặc trình duyệt đang ngắt -> kết quả không chứa chuỗi `browser_`; skill đòi tool không được gắn bị loại khỏi chỉ mục).
- [ ] **Chạy thấy fail.** **Hiện thực.** `prompts/kernel.md` viết bằng tiếng Anh, đủ 9 ý bắt buộc ở spec 8.4.6, <= 6.000 ký tự.
- [ ] **Chạy pass + commit:** `feat(prompt): bounded system prompt and run message [TaskID: P1.12]`.

### P1.13: Tool của Max và lệnh `max mcp`

**Files:** Create `internal/adapter/mcpserver/server.go`, `tools_core.go`, `runid.go`, `stdio.go` + test; `internal/app/tools/core.go`

**Interfaces:** Consumes: P1.06 (`Decide`), P1.08 (`Wake`). Produces:
```go
// app/tools
type Caller struct { RunID, AgentID, ConversationID domain.ID; TaskID *domain.ID; Policy domain.AgentPolicy; Origin domain.RootOrigin }
type Result struct { Text string; IsError bool }
func (s *Service) Ping(ctx context.Context, c Caller) Result
func (s *Service) NotifyOwner(ctx context.Context, c Caller, text, urgency string) Result
func (s *Service) AskOwner(ctx context.Context, c Caller, question string, choices []string) Result   // tạo inbox_items kind=question; đặt conversations.waiting_for
func (s *Service) NoteUpdate(ctx context.Context, c Caller, content string) Result                    // <= 80 dòng, ghi agents/<slug>/notes/NOTES.md
// adapter/mcpserver
func ResolveCaller(ctx context.Context, token string, peerPID int) (tools.Caller, error)   // có cả token và peerPID thì phải khớp cùng một run; thiếu token thì theo cây tiến trình
func RunStdio(ctx context.Context, sock string) error                                       // thân của lệnh `max mcp`
```
- [ ] **Test:** `TestPing` (trả văn bản chứa slug agent); `TestNoteUpdateCap` (81 dòng -> `IsError`, file không đổi; 80 dòng -> ghi được); `TestAskOwnerParks` (tạo 1 `inbox_items`, `waiting_for.kind='owner'`, kết quả chứa câu "kết thúc lượt"); `TestAskOwnerSingleQuestion` (gọi lần hai trong cùng run -> `IsError` "chỉ một câu hỏi mỗi lượt"); `TestResolveByToken` (token của run đang chạy -> đúng `Caller`; token của run đã kết thúc -> lỗi); `TestResolveByProcessTree` (sinh `fakecli` làm cha, tiến trình con gọi vào với PID của nó -> đúng run của cha; PID không thuộc run nào -> lỗi); `TestResolveRejectsCrossRunToken` (token của run B nhưng `peerPID` thuộc cây tiến trình của run A -> lỗi, chống đọc trộm `/proc/<pid>/environ`); `TestToolResultCap` (kết quả 20.000 ký tự -> văn bản trả về <= 8.000 và có đường dẫn file đầy đủ dưới `agents/<slug>/artifacts/_tool/`).
- [ ] **Chạy thấy fail.** **Hiện thực:** server MCP bằng go-sdk; `max mcp` là cầu stdio <-> unix socket `<DataDir>/run/max.sock`, gửi kèm `MAX_RUN_TOKEN` nếu có trong môi trường; phía daemon lấy `peerPID` từ peer credential của unix socket (`SO_PEERCRED` trên Linux) thay vì tin PID tự khai.
- [ ] **Chạy pass + commit:** `feat(mcp): max tool server with per-run caller identity [TaskID: P1.13]`.

### P1.14: Duyệt - dừng và tiếp tục

**Files:** Create `internal/app/approvals/service.go`, `service_test.go`

**Interfaces:** Consumes: P1.08 (`Wake`). Produces:
```go
type Request struct { Caller tools.Caller; ToolName string; Input json.RawMessage; Preview, Reason string; ByMax bool }  // ByMax: Max tự thực thi khi được duyệt
func (s *Service) Ask(ctx context.Context, r Request) (approvalID domain.ID, alreadyPending bool, err error)
func (s *Service) Decide(ctx context.Context, approvalID domain.ID, allow bool, decisionKey string) error
func (s *Service) ConsumeOnce(ctx context.Context, tx app.Tx, agentID domain.ID, tool, inputHash string) (bool, error)
func (s *Service) ExpireDue(ctx context.Context, now time.Time) (int, error)
type Executor func(ctx context.Context, c tools.Caller, tool string, input json.RawMessage) tools.Result   // do P3 cấp cho tool lớp E; P1 dùng cho test
```
- [ ] **Test:** `TestAskCreatesPendingAndParks` (1 dòng `pending`, `expires_at = now + 24h`, `waiting_for.kind='approval'`, có dòng outbox chủ đề `inbox`); `TestAskSameInputTwice` (lần hai trả cùng id, `alreadyPending=true`, không tạo dòng mới); `TestDecideAllowByMaxExecutesOnce` (`ByMax=true`: `Executor` được gọi đúng 1 lần với input nguyên văn; có wakeup `approval_result` chứa kết quả); `TestDecideIdempotent` (gọi `Decide` 2 lần cùng `decisionKey` -> `Executor` vẫn 1 lần, không lỗi; 2 lần khác `decisionKey` -> lần hai lỗi "đã quyết định"); `TestDecideDeny` (wakeup `approval_result` mang lý do; `Executor` không được gọi); `TestConsumeOnce` (`ByMax=false`, đã `allowed`: lần đầu khớp băm trả true và trạng thái thành `consumed`; lần hai false; băm khác false); `TestExpire` (quá hạn -> `expired`, có wakeup `approval_result` "không ai duyệt"); `TestPreviewRedacted` (input chứa `Bearer abc` -> `preview` không chứa `abc`, dài <= 600).
- [ ] **Chạy thấy fail.** **Hiện thực** theo spec 8.6 "Luồng duyệt".
- [ ] **Chạy pass + commit:** `feat(approvals): stop-and-resume approvals with exactly-once execution [TaskID: P1.14]`.

### P1.15: Kiểm sau run

**Files:** Create `internal/adapter/scopeaudit/audit.go`, `audit_test.go`

**Interfaces:** Consumes: P1.05. Produces:
```go
type Violation struct { Rel, Reason string; Reverted bool }
func Audit(ctx context.Context, vaultDir string, scope domain.AgentScope) (touched []string, violations []Violation, err error)
```
- [ ] **Test (repo git tạm):** `TestInScopeUntouched` (sửa `agents/a/notes/x.md` -> `touched` có nó, 0 vi phạm, file giữ nội dung mới); `TestOutOfScopeModifiedReverted` (sửa `company/COMPANY.md` -> 1 vi phạm, nội dung trở về bản đã commit); `TestOutOfScopeNewFileRemoved` (tạo `agents/b/notes/y.md` -> bị xóa); `TestSymlinkEscape` (tạo symlink `agents/a/notes/link -> /tmp/outside` rồi ghi qua nó -> symlink bị xóa, vi phạm được ghi, file ngoài vault không bị Max đụng tới nhưng được nêu trong `Reason`); `TestBinaryOutsideArtifacts` (file nhị phân trong `agents/a/notes/` -> vi phạm; trong `agents/a/artifacts/` -> được); `TestOversizeFile` (file 6 MB trong phạm vi -> vi phạm "quá 5 MB", bị gỡ); `TestSecretInFileQuarantined` (file trong phạm vi chứa `sk-ant-...`, khối PEM, hoặc `https://u:p@host`: không nằm trong `touched`, bị chuyển sang `<DataDir>/quarantine/<run>/`, có vi phạm "chứa secret"; file sạch cùng run vẫn được commit). `Audit` nhận thêm tham số `quarantineDir string`.
- [ ] **Chạy thấy fail.** **Hiện thực:** `git status --porcelain=v1 -z`; `git checkout -- <path>` cho file đã theo dõi, xóa file mới; không theo symlink.
- [ ] **Chạy pass + commit:** `feat(scopeaudit): post-run scope audit and revert [TaskID: P1.15]`.

### P1.16: Vault và git

**Files:** Create `internal/adapter/vaultgit/git.go`, `install.go` + test; `vault-template/` (`CLAUDE.md`, `company/COMPANY.md`, `teams/core/TEAM.md`, `agents/chief-of-staff/AGENT.md`, `memory/MEMORY.md`, thư mục rỗng có `.gitkeep`)

**Interfaces:** Produces:
```go
type Vault interface {
    EnsureInstalled(ctx context.Context) error                       // git init + chép vault-template nếu thư mục trống
    CommitPaths(ctx context.Context, paths []string, msg string) (sha string, err error)
    PullIfIdle(ctx context.Context, runsActive int) error
    Push(ctx context.Context) error
    Read(ctx context.Context, rel string) ([]byte, error)
    RefuseIfProjectSettings(ctx context.Context) error               // lỗi nếu có .claude/settings*.json
}
```
- [ ] **Test:** `TestInstallOnEmptyDir` (sau đó là repo git có 1 commit, có `agents/chief-of-staff/AGENT.md`); `TestInstallSkipsNonEmpty`; `TestCommitPathsOnlyThose` (2 file đổi, commit 1 -> file kia vẫn dirty); `TestCommitMessageNoAIAttribution` (thông điệp và tác giả không chứa các chuỗi `claude`, `anthropic`, `gemini`, `openai`, `co-authored-by` không phân biệt hoa thường; tác giả là `Max <max@localhost>`); `TestPullSkippedWhenRunsActive` (`runsActive=1` -> không gọi git); `TestPullConflictGoesToBranch` (remote và local sửa cùng dòng -> rebase bị hủy, thay đổi local nằm ở nhánh `max/conflict-*`, nhánh chính khớp remote, trả lỗi có kiểu `ErrConflict`); `TestRefuseProjectSettings`.
- [ ] **Chạy thấy fail.** **Hiện thực:** gọi binary `git`; một mutex cho mọi lệnh.
- [ ] **Chạy pass + commit:** `feat(vault): git-backed vault with path commits and template install [TaskID: P1.16]`.

### P1.17: Đồng bộ `AGENT.md` vào bảng `agents`

**Files:** Create `internal/app/agentsync/sync.go`, `frontmatter.go` + test

**Interfaces:** Consumes: `Vault.Read`. Produces:
```go
type AgentFile struct {
    Name, Template, Team, Manager string
    Engines   []domain.EngineName
    Models    map[domain.EngineName]string
    Autonomy  domain.Autonomy
    Web       bool
    Browser   domain.BrowserProfile
    Skills    []string
    QuotaTokensDay *int64
    Heartbeat string
    Params    map[string]any
    AllowUngatedEngine bool
    Body      string
}
func Parse(raw []byte) (AgentFile, error)
func (s *Service) SyncAll(ctx context.Context) (updated int, problems []Problem, err error)
```
- [ ] **Test:** `TestParseExample` (ví dụ frontmatter ở spec 8.9.2 -> đúng từng trường; `engines` thiếu -> `[agy]`); `TestParseRejects` bảng: thiếu dấu `---` đóng; YAML sai; `autonomy: full`; `engines: [gpt]`; byte không phải UTF-8; thân > 200 dòng (cảnh báo, không lỗi) -> mỗi trường hợp trả lỗi/cảnh báo nêu rõ trường; `TestSyncKeepsOldOnError` (agent đã có trong DB; file bị hỏng -> dòng DB không đổi, 1 `Problem`, 1 mục Inbox, các agent khác vẫn được đồng bộ); `TestSyncSetsMask` (`engines: [agy, claude]` -> `engine_mask=5`); `TestSyncIdempotent` (chạy hai lần -> lần hai `updated=0`, so theo `config_rev`); `TestRejectsUngatedSensitive` (agent có `browser: owner` và engine mức 3 mà `allow_ungated_engine` false -> `Problem`, giữ cấu hình cũ).
- [ ] **Chạy thấy fail.** **Hiện thực.**
- [ ] **Chạy pass + commit:** `feat(agentsync): AGENT.md to agents table with safe fallback [TaskID: P1.17]`.

### P1.18: Driver `agy`

**Files:** Create `internal/adapter/engine/agy/driver.go`, `acp.go`, `print.go`, `map.go`, `health.go` + test; `internal/testutil/fakecli/acp.go` (chế độ giả lập `agy acp`)

**Interfaces:** Consumes: P1.10; kết quả `docs/spikes/S0c.md` (chế độ A mặc định, tên trường usage, danh sách tool không hỏi quyền, cơ chế lưu map `toolCallId`) và `docs/spikes/S5.md` (mẫu nhận diện hết hạn mức). Produces: `agy.New(bin, home string, mode Mode, log *slog.Logger) app.Engine` với `Mode` là `ModeACP` hoặc `ModePrint`.

- [ ] **Test (với `fakecli` giả lập ACP):** `TestACPHappyPath` (thứ tự yêu cầu gửi đi: `initialize`, `session/new` có `cwd` và `mcpServers[0].env` chứa `{name: "MAX_RUN_TOKEN", value: <token của run>}`, `session/prompt`; sự kiện nhận về: `session`, `text_delta`..., `final` với số token); `TestACPResume` (`SessionID` khác rỗng -> gửi `session/load` kèm `mcpServers` mang `MAX_RUN_TOKEN` của run mới, không gửi `session/new`, và bỏ qua mọi `session/update` phát lại trước lúc gửi `session/prompt`); `TestACPSuggestSetsPlanMode` (`Autonomy == Suggest` -> gửi `session/set_mode` với `modeId: "plan"` trước `session/prompt`); `TestACPPermissionRoundTrip` (giả lập gửi `session/request_permission` cho `run_command` kể cả khi `ToolCallUpdate` lược bớt `kind` phải tra ngược theo `toolCallId`: `PermissionFunc` được gọi; trả `Allow` -> chọn đúng option có `kind == "allow_once"`; trả `Deny` hoặc gặp `kind` lạ/rỗng -> chọn đúng `kind == "reject_once"`; không có option khớp -> trả `outcome: "cancelled"`); `TestACPSystemPromptAppended`; `TestLimitEvent` (giả lập phát mẫu "hết hạn mức" đã ghi ở `docs/spikes/S5.md` -> sự kiện `limit`); `TestEnvIsolation` (tiến trình con có `HOME` = thư mục riêng và các biến đường dẫn người dùng cô lập, không lọt biến ngoài danh sách cho phép); `TestHealthForeignMCP` (HOME riêng có cấu hình MCP lạ -> `Health().ForeignMCP` khác rỗng; lưu ý kiểm `-32000` chỉ hợp lệ khi chạy dưới user OS riêng không có keyring chung hoặc trong container); `TestCapsByMode` (`ModeACP`: `PerRunMCP=true`, `PreToolGate=false`, `ToolAllowlist=false`, `Resume=true`; `ModePrint`: `PerRunMCP=false`, `PreToolGate=false`); `TestPrintMode` (script stream-json mẫu -> cùng chuỗi `EngineEvent` như ACP).
- [ ] **Chạy thấy fail.** **Hiện thực** theo spec 8.4.4; một tiến trình `agy` cho mỗi run (`Setpgid: true`); khi kết thúc hạ cả nhóm tiến trình `-pgid` và chờ xong goroutine đọc `stdout`/`stderr` trước khi `Wait()`. Không viết lại giao thức theo trí nhớ: dùng đúng tên phương thức và trường đã ghi trong `docs/spikes/S0c-trace-sample.ndjson`.
- [ ] **Test tích hợp thật (gắn nhãn build `agyreal`, không chạy trong `make check`):** `TestRealPing` (một lượt gọi tool `ping`).
- [ ] **Chạy pass + commit:** `feat(engine): agy driver over ACP with print fallback [TaskID: P1.18]`.

### P1.19: Chỗ chạy và sổ hạn mức

**Files:** Create `internal/app/limits/slots.go`, `quota.go` + test

**Interfaces:** Consumes: P1.07. Produces:
```go
func (s *Slots) Free() map[domain.EngineName]int
func (s *Slots) Acquire(e domain.EngineName) bool
func (s *Slots) Release(e domain.EngineName)
func (q *Quota) View(ctx context.Context, e domain.EngineName, now time.Time) (domain.QuotaView, error)
func (q *Quota) Record(ctx context.Context, tx app.Tx, e domain.EngineName, agentID domain.ID, in, out int64, now time.Time) error
func (q *Quota) OnLimit(ctx context.Context, e domain.EngineName, resetAt *time.Time, now time.Time) error
func (q *Quota) Admit(e domain.EngineName, p domain.Priority) domain.Admission     // dùng làm actor.Capacity.Admit
```
- [ ] **Test:** `TestSlotsNeverExceed` (100 goroutine tranh 3 chỗ: số đang giữ không bao giờ > 3); `TestInteractiveReservation` (3 chỗ: việc nền chỉ lấy được 2; `PrioInteractive` lấy được cái thứ 3); `TestRecordRollsWindows` (ghi ở hai cửa sổ ngắn khác nhau -> 2 dòng `quota_ledger`; `View` chỉ cộng cửa sổ hiện tại); `TestOnLimitLearns` (đã dùng 800 -> `est_short_tokens=800`, `limited_until` đặt đúng); `TestAdmitAfterReset` (`limited_until` đã qua -> Admit, và `est` vẫn giữ); `TestMemoryGuard` (hàm đọc bộ nhớ trống trả < 1 GB -> `Free()` trả 0 cho mọi engine).
- [ ] **Chạy thấy fail.** **Hiện thực.**
- [ ] **Chạy pass + commit:** `feat(limits): run slots and learned subscription quota ledger [TaskID: P1.19]`.

### P1.20: Scheduler nội bộ và vòng điều phối

**Files:** Create `internal/app/scheduler/tick.go`, `internal/app/dispatch/loop.go` + test

**Interfaces:** Consumes: P1.09, P1.11, P1.14, P1.19. Produces:
```go
func (s *Scheduler) Tick(ctx context.Context, now time.Time) error      // gọi mỗi 30 giây
func (d *Dispatcher) Run(ctx context.Context) error                     // vòng: có chỗ hoặc có tín hiệu Wake -> ClaimBatch -> go Execute
func (d *Dispatcher) Notify()                                           // gọi sau mỗi Wake và mỗi lần run kết thúc
```
- [ ] **Test:** `TestTickPromotesDueWakeups` (wakeup `not_before` đã qua -> agent `ready=1`); `TestTickExpiresApprovals`; `TestTickReconcilesTeamCounter` (đặt sai `teams.running=5` khi không có run -> về 0); `TestTickRetention` (wakeup `done` 31 ngày -> bị xóa; 29 ngày -> còn); `TestTickClockJumpForward` (now nhảy +3 ngày: mỗi wakeup hẹn giờ được nâng đúng 1 lần, không tạo bản sao); `TestTickClockJumpBackward` (now lùi 2 giờ: không lỗi, không dòng `quota_ledger` nào có `window_start` ở tương lai được tạo thêm); `TestDispatcherRunsUpToSlots` (5 agent sẵn sàng, 3 chỗ, engine giả ngủ 50 ms -> đỉnh số run đồng thời là 3, cả 5 xong); `TestDispatcherShutdown` (hủy context -> các run đang chạy bị hủy, kết thúc `interrupted`, hàm trả về trong < 15 giây).
- [ ] **Chạy thấy fail.** **Hiện thực.**
- [ ] **Chạy pass + commit:** `feat(scheduler): internal tick and dispatcher loop [TaskID: P1.20]`.

### P1.21: Hợp đồng API và xác thực

**Files:** Create `api/openapi.yaml` (các nhóm Auth, Chat, Run, Duyệt, Inbox, Mức dùng, Hệ thống, Sự kiện của spec 8.12), `internal/adapter/httpapi/gen/` (sinh), `server.go`, `auth.go`, `middleware.go` + test; lệnh con `max admin set-password`

**Interfaces:** Produces: `httpapi.New(deps Deps) http.Handler`; middleware `RequireSession`, `CheckOrigin`, `CheckHost`, `SecurityHeaders`, `RateLimitLogin`.

- [ ] **Test:** `TestLoginSetsCookie` (`HttpOnly`, `SameSite=Strict`; `Secure` khi `PublicURL` là https); `TestSessionStoredHashed` (bảng `auth_sessions` không chứa token thô); `TestWrongPassword401`; `TestLoginRateLimit` (lần thử thứ 6 trong 1 phút từ cùng IP -> 429); `TestProtectedRouteNeedsSession` (mọi route trừ `healthz` và `login` -> 401 khi không có cookie; test duyệt qua toàn bộ route trong spec sinh ra); `TestOriginCheck` (POST với `Origin` khác `PublicURL` -> 403; không có `Origin` -> được); `TestHostAllowlist` (`Host: evil.example` -> 400); `TestSecurityHeaders` (CSP có `default-src 'self'`, không `unsafe-inline`; `X-Content-Type-Options: nosniff`); `TestNoPublicSetup` (không tồn tại route tạo tài khoản; khi chưa có user và không có `MAX_ADMIN_PASSWORD`, `login` trả 503 kèm hướng dẫn chạy `max admin set-password`); `TestPasswordMinLength` (11 ký tự bị từ chối, 12 được).
- [ ] **Chạy thấy fail.** **Hiện thực:** argon2id; token 32 byte ngẫu nhiên, lưu SHA-256; phiên 30 ngày; `oapi-codegen` chế độ strict server.
- [ ] **Chạy pass + commit:** `feat(api): openapi contract, session auth and web hardening [TaskID: P1.21]`.

### P1.22: Chat

**Files:** Create `internal/app/chat/service.go` + test; `internal/adapter/httpapi/handlers_chat.go` + test

**Interfaces:** Consumes: P1.08. Produces:
```go
func (s *Service) CreateThread(ctx context.Context, agentID domain.ID, title string) (domain.ID, error)
func (s *Service) Send(ctx context.Context, threadID domain.ID, content string, attachments []string, idemKey string) (messageID, wakeupID domain.ID, err error)
func (s *Service) Reset(ctx context.Context, threadID domain.ID) error      // xóa sessions của conversation
func (s *Service) Snapshot(ctx context.Context, threadID domain.ID) (ThreadSnapshot, error)   // message + run đang chạy + text tạm + agent bận việc gì
```
- [ ] **Test:** `TestSendCreatesMessageAndWake` (cùng một giao dịch: 1 `messages` vai `user`, 1 wakeup `message` ưu tiên 100, conversation được tạo nếu chưa có); `TestSendIdempotent` (cùng `idemKey` hai lần -> 1 message, 1 wakeup); `TestSendWhileBusy` (agent đang chạy run khác: message vẫn lưu, snapshot báo `busyWith` có tên việc và số phút); `TestResetClearsSessions`; handler: `TestSendMessage202`, `TestListMessagesPagination` (con trỏ `before`, tối đa 100).
- [ ] **Chạy thấy fail.** **Hiện thực.**
- [ ] **Chạy pass + commit:** `feat(chat): owner threads with durable send [TaskID: P1.22]`.

### P1.23: SSE theo chủ đề và stream run

**Files:** Create `internal/adapter/httpapi/sse.go`, `hub.go` + test; `internal/platform/eventbus.go`

**Interfaces:** Produces:
```go
type Event struct { Topic, Type string; Data any }
func (b *Bus) Publish(e Event)
func (b *Bus) Subscribe(topics []string, buf int) (<-chan Event, func())
func (h *Hub) RunText(runID domain.ID) (snapshot string, deltas <-chan string, cancel func())   // text tạm, tối đa 256 KB
```
- [ ] **Test:** `TestTopicFiltering` (đăng ký `thread:1` không nhận sự kiện `thread:2`); `TestNoWildcardTopic` (yêu cầu chủ đề `*` hoặc rỗng -> 400); `TestSlowClientDropped` (không đọc, phát 300 sự kiện với buffer 256 -> kết nối bị đóng, bộ phát không bị chặn); `TestCountersThrottled` (100 lần đổi số đếm trong 100 ms -> tối đa 2 sự kiện `status`); `TestRunTextCap` (đẩy 300 KB -> snapshot dài 256 KB, là phần đuôi); `TestPing` (có dòng comment mỗi 20 giây; test dùng đồng hồ giả).
- [ ] **Chạy thấy fail.** **Hiện thực.**
- [ ] **Chạy pass + commit:** `feat(api): topic-scoped SSE and per-run text stream [TaskID: P1.23]`.

### P1.24: Các endpoint còn lại của Phase 1

**Files:** Create `internal/adapter/httpapi/handlers_{approvals,inbox,usage,system,runs}.go` + test

**Interfaces:** Consumes: P1.14, P1.19, P1.20. Hiện thực các `operationId`: `listApprovals`, `decideApproval`, `listInbox`, `markInboxRead`, `answerOwnerQuestion`, `getUsage`, `getQuota`, `updateQuotaSettings`, `getStatus`, `getSettings`, `updateSettings`, `listRuns`, `getRun`, `listRunEvents`, `cancelRun`, `streamRun`, `healthz`.

- [ ] **Test:** `TestDecideApprovalRequiresIdempotencyKey` (thiếu header -> 400); `TestDecideApprovalTwiceSameKey` (200 hai lần, hiệu lực một lần); `TestAnswerOwnerQuestionWakes` (tạo wakeup `owner_reply`, xóa `waiting_for`); `TestCancelRunRequeueOrDrop` (tham số `mode=requeue` -> wakeup về `queued`; `mode=drop` -> `dropped`); `TestSettingsEngines` (đặt số chỗ `agy=9` -> 400; `=4` -> lưu vào `kv`, `Slots` phản ánh ngay; tắt engine duy nhất đang bật -> 400); `TestStatusShape` (có `engines[].{name, installed, loggedIn, slots, used, limitedUntil}`, `queue.byPriority`, `deps`); `TestNoSecretsInResponses` (quét mọi phản hồi GET của các handler trong test: không chứa giá trị của `AdminPassword` hay biến secret nào đã nạp); `TestListsPaginated` (mọi danh sách từ chối `limit > 100`); `TestAuditLogOnOwnerMutations` (`decideApproval`, `updateSettings`, `cancelRun` mỗi lời gọi thành công thêm đúng 1 dòng `audit_log` có `action` tương ứng; chi tiết đã qua `Redact`; không endpoint nào sửa hay xóa `audit_log`).
- [ ] **Chạy thấy fail.** **Hiện thực.**
- [ ] **Chạy pass + commit:** `feat(api): approvals, inbox, usage, system and run endpoints [TaskID: P1.24]`.

### P1.25: Khung UI

**Files:** Create `web/` (SvelteKit SPA): `package.json`, `svelte.config.js`, `vite.config.ts`, `src/app.html`, `src/routes/+layout.ts`, `+layout.svelte`, `src/lib/api/client.ts`, `src/lib/state/{session,events}.svelte.ts`, `src/lib/components/MarkdownView.svelte`, `src/app.css` (chép nguyên `docs/superpowers/design/tokens.css` sau `@import "tailwindcss"`; nạp `@fontsource/inter` 400, 500, 600), `src/lib/i18n/vi.ts`; `internal/adapter/httpapi/static.go`; đích `make web`

**Interfaces:** Produces: `api.<operationId>(...)` có kiểu; `session` (`user`, `login()`, `logout()`); `events.subscribe(topics, handler)` trên đúng một `EventSource`; `<MarkdownView source />`; class Tailwind theo token (`bg-surface`, `text-muted`, `border-border-strong`...); `t(key)` đọc chuỗi từ `vi.ts`.

- [ ] **Trước khi viết:** tra Svelte MCP các mục về runes, `$state` trong module `.svelte.ts`, SvelteKit SPA/adapter-static, snippet. Dùng cấu hình đã chứng minh ở `docs/spikes/S3.md`.
- [ ] **Test (vitest):** `client.test.ts` (lỗi HTTP thành `ApiError` có `code`; mọi lệnh ghi tự gắn `Idempotency-Key`); `events.test.ts` (hai lần `subscribe` vẫn chỉ tạo 1 `EventSource`; mất kết nối -> gọi hàm `resync` rồi nối lại); `MarkdownView.test.ts` (đầu vào `<script>alert(1)</script>` và `<img onerror=...>` không xuất hiện trong DOM; link ngoài có `rel="noopener noreferrer"`).
- [ ] **Test Go:** `TestSPAFallback` (`GET /activity` trả `index.html`; `GET /api/v1/nope` trả 404 JSON, không phải HTML).
- [ ] **Chạy thấy fail.** **Hiện thực:** `ssr = false`; type sinh bằng `openapi-typescript`; sanitize bằng một thư viện sanitize HTML đã ghim phiên bản; chạy `svelte-autofixer` trên mọi file `.svelte`/`.svelte.ts`.
- [ ] **Chạy pass + commit:** `bun run check && bun run lint && bun run test && make web && make check`; `feat(web): spa scaffold, typed client, single event stream [TaskID: P1.25]`.

### P1.31: Design system (làm ngay sau P1.25, trước P1.26)

**Files:** Create `web/src/lib/ui/{Button,StatusBadge,Field,Input,Textarea,Select,Card,ListRow,DataTable,Dialog,Menu,Toast,EmptyState,Skeleton,Tree,AppShell}.svelte`, `web/src/lib/ui/status.ts`, `web/src/lib/ui/toast.svelte.ts`, `web/src/lib/ui/index.ts`, `web/src/routes/design-system/+page.svelte` + test cạnh từng file

**Interfaces:** Consumes: token và `t()` của P1.25. Produces (mọi component nhận `class` để chỉnh bố cục, không nhận prop màu):
- `<Button variant="outline|primary|secondary|ghost|danger" icon={Component} loading disabled iconOnly ariaLabel onclick>`.
- `statusMeta(kind: 'run'|'task'|'approval'|'agent'|'engine'|'tool', status: string, reason?: string): { tone: 'neutral'|'warning'|'error'|'success', icon: Component, labelKey: string, spin: boolean }` trong `status.ts`; `<StatusBadge kind status reason />`.
- `<Field label hint error for>` bọc `<Input>`, `<Textarea>`, `<Select options>`; cả ba nhận `bind:value`, `invalid`, `disabled`.
- `<Card title flush>` với snippet `action` và `children`.
- `<ListRow title meta initials href>` với snippet `trailing`.
- `<DataTable rows columns rowKey onEndReached>`; `columns: { key, labelKey, align?: 'right', width?, cell?: Snippet<[row]> }[]`; dòng cao cố định 40px.
- `<Dialog bind:open title busy>` với snippet `children`, `actions`.
- `<Menu items>`; `items: { labelKey, icon?, danger?, onselect }[]`; snippet `trigger`.
- `toast.show({ kind: 'info'|'error', messageKey })`; `<Toast />` gắn một lần trong layout.
- `<EmptyState message>` với snippet `action`; `<Skeleton lines>`.
- `<Tree nodes bind:expanded>`; `nodes: { id, label, children? }[]`; snippet `row`.
- `<AppShell items inboxCount current>` với snippet `topbar`, `children`.

- [ ] **Trước khi viết:** tra Svelte MCP (snippet và `{@render}`, `$props`, `$bindable`, `{#each}` có khóa, action/attachment cho phần tử gốc). Mở `docs/superpowers/design/design-system.html` cạnh màn hình; class và kích thước lấy từ đó.
- [ ] **Test (vitest + testing-library):**
  - `status.test.ts`: mọi giá trị trạng thái của run, task, approval, agent trong `openapi.yaml` có một mục trong `statusMeta`; `quota_stopped` và `interrupted` có `tone: 'neutral'`; `running` có `spin: true`; `blocked` với `reason` lần lượt `dependency|needs_owner|stalled|error` cho tone `neutral|warning|error|error`; trạng thái lạ trả tone `neutral` và `labelKey` là nguyên văn trạng thái.
  - `StatusBadge.test.ts`: badge luôn có chữ; icon có `aria-hidden`.
  - `Button.test.ts`: `loading` thì bấm không gọi `onclick`, nút có `aria-busy="true"` và không có thuộc tính `disabled`; `iconOnly` thiếu `ariaLabel` thì ném lỗi lúc dev.
  - `Field.test.ts`: bấm nhãn thì ô nhận tiêu điểm; có `error` thì ô có `aria-invalid="true"` và `aria-describedby` trỏ tới câu lỗi; không có `error` thì dòng dưới ô vẫn tồn tại trong DOM.
  - `DataTable.test.ts`: 10.000 dòng thì số `<tr>` trong DOM < 60; cuộn tới cuối gọi `onEndReached` đúng 1 lần cho mỗi lần chạm đáy; cột `align: 'right'` có class `tabular-nums`.
  - `Dialog.test.ts`: `open = true` gọi `showModal()`; sự kiện `cancel` (Esc) đặt `open = false`; khi `busy` thì Esc không đóng.
  - `Menu.test.ts`: chọn mục gọi `onselect` rồi đóng; mũi tên xuống/lên đổi mục đang trỏ; mục `danger` không có class màu đỏ khi chưa rê.
  - `toast.test.ts`: `kind: 'info'` tự mất sau 5.000 ms (dùng fake timer); `kind: 'error'` còn nguyên sau 60.000 ms.
  - `Tree.test.ts`: nhánh đóng không render con; mũi tên phải mở, mũi tên trái đóng.
  - `AppShell.test.ts`: `inboxCount = 0` thì không render số đếm; `inboxCount = 120` hiện "99+"; mục `current` có `aria-current="page"`.
  - `tokens.test.ts`: không file nào trong `src/lib` và `src/routes` chứa mã màu viết cứng (`#rgb`, `#rrggbb`, `rgb(`, `hsl(`); không file nào import gói có tên chứa `lucide` khác `@lucide/svelte`.
- [ ] **Chạy thấy fail.** **Hiện thực** (mỗi file <= 300 dòng; `Dialog` dùng `<dialog>`, `Menu` dùng thuộc tính `popover` hoặc phương án đã ghi ở `docs/spikes/S3.md`; `DataTable` tự ảo hóa bằng chiều cao dòng cố định, không thêm thư viện; chạy `svelte-autofixer`).
- [ ] **Trang `/design-system`:** dựng lại đúng các khối của `docs/superpowers/design/design-system.html` bằng chính component vừa viết, mỗi trạng thái một ví dụ tĩnh. Chụp ảnh ở 375px và 1440px, đặt cạnh ảnh của file mẫu; lệch chỗ nào sửa component chỗ đó, không sửa file mẫu.
- [ ] **Chạy pass + commit:** `bun run check && bun run lint && bun run test`; `feat(web): design system primitives and preview route [TaskID: P1.31]`.

### P1.26: Trang chat

**Files:** Create `web/src/routes/(app)/+page.svelte`, `src/lib/state/threads.svelte.ts`, `src/lib/components/{ThreadList,MessageBubble,Composer,ToolChip,ApprovalCard,BusyBanner}.svelte` + test

**Interfaces:** Consumes: P1.25, component của P1.31 (`Button`, `StatusBadge`, `Card`, `AppShell`...), endpoint của P1.22-P1.24. `ApprovalCard`, `MessageBubble`, `ToolChip` theo mẫu ở khối "Thành phần riêng của Max" của `design-system.html`.

- [ ] **Trước khi viết:** tra Svelte MCP (snippet, `$effect`, xử lý form).
- [ ] **Test (vitest + testing-library):** `threads.test.ts` (sự kiện `run.text` nối vào bong bóng đang gõ của đúng thread; `message.created` thay bong bóng tạm; mất kết nối rồi `resync` không nhân đôi message); `ApprovalCard.test.ts` (bấm Cho phép hai lần nhanh -> đúng 1 request; sau khi xong thẻ chuyển trạng thái, nút bị khóa); `BusyBanner.test.ts` (hiện tên việc và số phút; nút "Dừng việc đó" gọi `cancelRun` với `mode=requeue`); `Composer.test.ts` (gửi rỗng bị chặn; Enter gửi, Shift+Enter xuống dòng).
- [ ] **Chạy thấy fail.** **Hiện thực** (mỗi file <= 300 dòng; chạy `svelte-autofixer`).
- [ ] **Chạy pass + commit:** `feat(web): chat page with inline approvals and busy state [TaskID: P1.26]`.

### P1.27: Trang inbox, mức dùng, hệ thống, đăng nhập

**Files:** Create `web/src/routes/login/+page.svelte`, `(app)/inbox/+page.svelte`, `(app)/usage/+page.svelte`, `(app)/system/+page.svelte`, `src/lib/state/{approvals,inbox,usage,status}.svelte.ts`, `src/lib/components/{EngineSettings,QuotaMeter,HealthDots}.svelte` + test

- [ ] **Trước khi viết:** tra Svelte MCP (điều hướng, tải dữ liệu phía client).
- [ ] **Test:** `EngineSettings.test.ts` (không cho đặt số chỗ ngoài 1-8; không cho tắt engine cuối cùng; engine chưa đăng nhập hiện lệnh đăng nhập và bị khóa nút bật); `QuotaMeter.test.ts` (trần chưa biết hiện "đang học", không hiện phần trăm; đang `limited` hiện giờ mở lại và số việc đang chờ); `inbox.test.ts` (huy hiệu đếm = số mục chưa đọc; trả lời câu hỏi của agent gọi `answerOwnerQuestion`).
- [ ] **Chạy thấy fail.** **Hiện thực.**
- [ ] **Chạy pass + commit:** `feat(web): inbox, usage, system settings and login pages [TaskID: P1.27]`.

### P1.28: Nối dây, `max serve`, `max doctor`

**Files:** Modify `cmd/max/main.go`; Create `cmd/max/serve.go`, `doctor.go`, `internal/app/run/permission.go` + test

**Interfaces:** Consumes: mọi task trước. Produces: `max serve`, `max doctor`, `max migrate`, `max healthcheck`; `run.NewPermissionFunc(...) app.PermissionFunc` (nối `policy.Decide` + `approvals.Ask` + `ConsumeOnce` + khóa trình duyệt ở Phase 3).

- [ ] **Test:** `TestPermissionFuncAllowRecordsTouch` (ghi file trong phạm vi -> Allow, đường dẫn vào `TouchedPaths`); `TestPermissionFuncAskParks` (lớp cần duyệt -> trả `Ask`, có dòng `approvals`); `TestPermissionFuncConsumesApproved` (đã được duyệt, cùng băm -> Allow đúng 1 lần); `TestPermissionFuncDeadRun` (token của run đã kết thúc -> Deny); `TestServeEndToEnd` (khởi `serve` với engine giả và thư mục tạm: đăng nhập, gửi tin, nhận `run.text` qua SSE, message trợ lý được lưu, vault có commit; tắt bằng SIGTERM trả mã 0); `TestServeRecoversOnBoot` (đặt sẵn 1 run `running` trong DB -> sau khởi động thành `interrupted` và wakeup được chạy lại); `TestDoctorReports` (engine giả báo `ForeignMCP` -> `doctor` thoát mã 1 và nêu tên server lạ; mọi thứ ổn -> mã 0); `TestBinaryChangeDisablesEngine` (ghi băm của `fakecli` vào `engine_binaries`, rồi sửa 1 byte của binary: lần kiểm kế tiếp đặt engine `unavailable`, phát `system.notice`, không run nào được nhận trên engine đó; sau `max engine confirm <tên>` băm mới được ghi và engine chạy lại).
- [ ] **Chạy thấy fail.** **Hiện thực:** thứ tự khởi động theo spec 8.15; `errgroup` cho vòng đời; engine có `ForeignMCP` khác rỗng hoặc chưa đăng nhập thì ở trạng thái `unavailable`.
- [ ] **Chạy pass + commit:** `feat(cmd): wire serve, doctor and permission gate [TaskID: P1.28]`.

### P1.29: `max loadtest` và kiểm bất biến

**Files:** Create `cmd/max/loadtest.go`, `internal/testutil/invariants/check.go` + test; `scripts/chaos.sh`

**Interfaces:** Produces: `max loadtest --agents N --wakeups M --slots K --kill-every D`; `invariants.Check(ctx, store) []Violation` cho I1-I12 của spec mục 11 (I13, I14 thêm ở kế hoạch B).

- [ ] **Test:** `TestInvariantsDetect` (với mỗi bất biến I1, I3, I5: dựng DB vi phạm cố ý -> `Check` báo đúng mã bất biến); `TestLoadtestSmall` (200 agent, 2.000 wakeup, 3 chỗ, CLI giả 5-20 ms -> 0 vi phạm, mọi wakeup `done`).
- [ ] **Chạy thấy fail.** **Hiện thực:** loadtest dùng `fakecli` làm engine; in throughput, p95 thời gian chờ nhận việc, RSS của tiến trình `max`.
- [ ] **Chạy đo (không nằm trong `make check`):** `max loadtest --agents 5000 --wakeups 100000 --slots 8` và `scripts/chaos.sh` (giết `max` mỗi 5 giây trong 2 phút rồi để chạy hết). Đạt khi 0 vi phạm, nhận việc p95 < 20 ms, RSS <= 512 MiB. Ghi kết quả vào `docs/loadtest/phase1.md`.
- [ ] **Commit:** `feat(loadtest): load and chaos harness with invariant checks [TaskID: P1.29]`.

### P1.30: Đóng gói và sao lưu

**Files:** Create `Containerfile`, `compose.yaml`, `deploy/max.service`, `scripts/backup.sh`, `scripts/restore.sh`, `docs/ops/runbook.md`, `.env.example`

- [ ] **Test:** `scripts/backup_restore_test.sh`: tạo dữ liệu mẫu, chạy `backup.sh` (dùng `VACUUM INTO` cho SQLite, tar `agent-home`), xóa thư mục dữ liệu, chạy `restore.sh`, kiểm số dòng `messages` khớp và `max doctor` thoát mã 0 (với engine giả).
- [ ] **Chạy thấy fail.** **Hiện thực:** image một container theo spec 8.15 cách A (chỉ `agy` ở Phase 1): user non-root, `cap_drop: ALL`, `no-new-privileges`, rootfs read-only, tmpfs cho thư mục run, publish `127.0.0.1:8787`. `runbook.md` ghi: đăng nhập `agy` lần đầu trong HOME riêng, đổi mật khẩu, sao lưu, khôi phục, nâng bản, xử lý khi mất đăng nhập.
- [ ] **Kiểm tay (ghi vào runbook):** build image; `docker compose run --rm max agy` để đăng nhập; `compose up -d`; gửi 1 tin thật; `compose restart` giữa một run và xác nhận run chạy tiếp.
- [ ] **Commit:** `chore(deploy): single-container image, systemd unit, backup and restore [TaskID: P1.30]`.

## Tiêu chí thoát Phase 1 (từ spec mục 13)

- [ ] Chat với `chief-of-staff` chạy `agy` thật trong 3 ngày liên tục.
- [ ] Giết `max` giữa một run: run chạy tiếp sau khởi động lại, không lặp tác động.
- [ ] Gói báo hết hạn mức: việc tự dừng và tự chạy lại lúc mở, chủ nhân nhận đúng 1 thông báo.
- [ ] `max loadtest` 5.000 agent / 100.000 wakeup: 0 vi phạm.
- [ ] Khôi phục từ bản sao lưu trên thư mục sạch thành công.
- [ ] Xóa `spikes/`.
