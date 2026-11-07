# Motty iOS 移植架构

方案 B：Go 核心 + 原生 SwiftUI。iOS 先行，同一套 Go 核心为后续 Android 保留 JNI 入口。

## 1. 目标与范围

完整移植桌面版 Motty 的能力，按 iOS 平台约束裁剪：

| 能力 | iOS | 说明 |
| --- | --- | --- |
| SSH 终端（多标签） | ✅ | 核心功能，`internal/sshconn` + `internal/term` 直接复用 |
| 主机管理/分组/标签/导入 | ✅ | 新建移动端存储层，导入复用 `internal/sshconfig` |
| 密码/私钥/跳板机/代理 | ✅ | 复用 `internal/sshconn`、`internal/vault`（Keychain） |
| SFTP 浏览/上传/下载 | ✅ | 复用 `internal/sftpclient` |
| 端口转发 | ✅ | 复用 `internal/portforward`；后台会随进程挂起 |
| 会话日志/命令历史/补全 | ✅ | 复用 `internal/sessionlog`、`internal/cmdhistory`、`internal/completion` |
| AI 助手 | ✅ | 复用 `internal/ai/*`；UI 用 SwiftUI 重写 |
| VNC / RDP | ✅ | 复用 `internal/rfb`、`internal/rdpcore`；触摸交互重设计 |
| 云同步（WebDAV/S3/Gist）/备份 | ✅ | 网络可用 |
| 本地 shell | ❌ | iOS 禁止 `fork/exec` |
| 串口 | ❌ | 无设备节点访问 |
| 应用内自更新 | ❌ | App Store 禁止 |
| X11 转发 | ❌ | 无 X server |
| AI CLI provider | ❌ | 不能启动外部进程 |
| MCP stdio | ❌ | 不能启动外部进程；MCP over HTTP 保留 |
| 多窗口/托盘/菜单 | ⚠️ | 改为 iOS 导航与快捷键 |
| 系统代理自动探测 | ⚠️ | 改为手动配置代理 |

## 2. 总体架构

```
┌──────────────────────────────────────────────────┐
│ SwiftUI App                                       │
│  Hosts / Terminal / SFTP / AI / Settings / RDP    │
│  TerminalView (UIKit + CoreText, 自绘)            │
├──────────────────────────────────────────────────┤
│ MottyBridge (Swift)                               │
│  RPC 客户端 · 事件轮询 · 终端输出轮询 · 二进制协议   │
├──────────────────────────────────────────────────┤
│ libmotty.a (Go, c-archive)                        │
│  mobile/             C ABI 入口                   │
│  internal/mobilecore 平台无关的应用服务            │
│  internal/*          现有纯 Go 能力                │
└──────────────────────────────────────────────────┘
```

### 2.1 为什么不用 gomobile

- 项目导出面大，gomobile 对复杂类型/回调的限制会反复改接口。
- 终端输出是高频二进制流，C ABI + 共享缓冲区比 gomobile 回调更直接。
- c-archive 的 ABI 稳定，后续 Android 用 JNI 包同一组函数即可。
- 不引入 `golang.org/x/mobile` 的版本兼容风险。

### 2.2 线程模型

- SwiftUI 主线程只做渲染与输入。
- 所有 `MottyCall` 在专用串行后台队列调用，避免阻塞主线程。
- Go 侧每个会话有独立的连接 goroutine；事件写入线程安全队列。
- Swift 用 `CADisplayLink`（或 60Hz Timer）轮询：终端输出、事件队列、AI 流。

## 3. Go 侧目录

```
mobile/                     package main, build tag ios && cgo
  main_ios.go              //export MottyCall / MottyPollEvent / MottyDrain ...
  stub_other.go            桌面构建的空 main，避免 go build ./... 失败

internal/mobilecore/
  app.go                   生命周期、目录、RPC 分发、数据迁移入口
  rpc.go                   方法注册与 JSON 编解码
  store.go                 config.db（configstore，format 23）+ vault + 迁移
  host.go                  主机模型与导入（桌面 savedHost 字段）
  keys.go                  存储型私钥（vault sshkey:*）与 legacy 迁移
  session.go               连接状态机、认证、重连
  screen.go                终端输出/输入/屏幕快照编码
  files.go                 SFTP 会话与传输
  forward.go               端口转发
  logs.go                  会话日志（sessionlog，SQLite）
  history.go               命令历史与补全
  ai_run.go                AI run、工具审批、流式事件
  ai_backend.go            gate.Backend 实现（终端/SFTP 工具）
  ai_settings.go           provider/模型/API key（key 存 vault）
  desktop.go               VNC/RDP 适配与帧缓冲
  sync.go                  云同步、本地/远端备份与恢复
  keystore_ios.go          Keychain 实现（cgo，Security.framework）
  keystore_other.go        桌面测试用文件 keystore
  events.go                事件队列
```

## 4. 桥接协议

### 4.1 控制面 RPC

```c
char *MottyCall(const char *method, const char *paramsJSON);
void  MottyFree(char *s);
```

返回统一 JSON：

```json
{"ok":true,"result":{...}}
{"ok":false,"error":{"code":"not_found","message":"..."}}
```

方法清单（全部已实现）：

| 方法 | 说明 |
| --- | --- |
| `app.info` | 版本、平台、目录 |
| `hosts.list` / `hosts.get` / `hosts.save` / `hosts.delete` / `hosts.groups` | 主机管理 |
| `hosts.passwordStatus` / `hosts.password` | 主机密码（vault `host:<id>:password`，不进入 Host 记录） |
| `hosts.importSSHConfig` | 导入 OpenSSH config |
| `keys.list` / `keys.import` / `keys.delete` | 私钥导入（base64）与删除，存 vault |
| `sessions.open` / `sessions.close` / `sessions.list` / `sessions.reconnect` | 会话 |
| `sessions.input` / `sessions.resize` / `sessions.answer` / `sessions.scroll` / `sessions.scrollTo` | 输入、尺寸、主机密钥/密码/passphrase应答、按行/绝对位置滚动 |
| `files.list` / `files.stat` / `files.download` / `files.upload` / `files.create` / `files.mkdir` / `files.remove` / `files.rename` / `files.chmod` / `files.symlink` | SFTP 文件与传输；`files.create` 只新建空文件，拒绝覆盖同名文件 |
| `files.openText` / `files.saveText` | 远端 UTF-8 文本编辑：8 MiB 上限、BOM 保留、保存时校验 revision |
| `files.transfers` / `files.cancel` / `files.pause` / `files.resume` / `files.retry` / `files.forget` / `files.release` | 传输控制 |
| `forwards.list` / `forwards.add` / `forwards.update` / `forwards.remove` / `forwards.start` / `forwards.stop` / `forwards.status` | 端口转发 |
| `logs.list` / `logs.events` / `logs.stats` / `logs.favorite` / `logs.delete` / `logs.clear` | 会话日志 |
| `history.list` / `history.delete` / `history.clear` / `completion.complete` | 命令历史与补全 |
| `desktop.open` / `desktop.close` / `desktop.status` / `desktop.pointer` / `desktop.wheel` / `desktop.key` / `desktop.text` / `desktop.clipboard.get` / `desktop.clipboard.set` / `desktop.answer` / `desktop.resize` / `desktop.active` | VNC/RDP |
| `ai.settings.get` / `ai.settings.save` / `ai.models` | 供应商列表与当前供应商（端点、key、模型、模式）；`ai.models` 可带未保存的草稿 |
| `ai.send` / `ai.cancel` / `ai.approve` / `ai.reset` | 对话与审批 |
| `ai.conversations` / `ai.conversation.get` / `ai.conversation.delete` | 按主机保存的对话历史：列表、打开继续、删除 |
| `sync.status` / `sync.configure` / `sync.clear` / `sync.test` / `sync.now` / `sync.restore` | 云同步 |
| `backup.export` / `backup.inspect` / `backup.restore` | `.mottybak` 导出与恢复 |

`sync.status` 永不返回密码，只返回 `configured`、`hasSecret`、`hasBackupPassword`、`lastSync`、`error`、`running` 等状态。网络操作在后台 goroutine 执行，前端通过 `sync.state` / `sync.done` / `sync.error` / `sync.test` 事件跟踪。

### 4.2 数据面

```c
// 终端屏幕快照：返回写入字节数；cap 不足时返回所需大小（负数）
int MottyScreenSnapshot(unsigned int sessionID, unsigned long long sinceVersion,
                        void *buf, int cap);

// 远程桌面帧：返回写入字节数；无变化返回 0；cap 不足返回负的所需大小；
// 桌面不存在返回 -1
int MottyDesktopFrame(unsigned int desktopID, unsigned long long sinceVersion,
                      void *buf, int cap);

// 事件队列：返回一条 JSON 事件，无事件返回 0
int MottyPollEvent(void *buf, int cap);
```

终端输出与 AI 流采用轮询而非跨语言回调，原因：

- 避免 Go goroutine 直接触碰 Swift/ObjC 对象的线程问题。
- 合并高频小数据，减少 CGO 切换。
- 终端输出在 `Shell` 内部已有环形缓冲，`Drain` 天然适配。

### 4.3 屏幕快照格式（小端）

```
header 48 bytes:
  u64 version      终端状态版本
  u32 cols, rows
  u32 cursorX, cursorY
  u32 cursorStyle  0 block, 1 bar, 2 underline
  u32 flags        bit0 alt screen, bit1 cursor visible, bit2 cursor steady,
                   bit3 bracketed paste, bit4 app cursor keys, bit5 reverse video,
                   bit6 mouse mode, bit7 synchronized output
  u32 scrollback   历史行数
  u32 viewOffset   当前回滚位置
  u32 reserved0, reserved1

每行 rows 次：
  u32 cellCount
  cellCount × 16 bytes:
    u32 rune
    u32 fg         term.Color：kind<<24 | value（kind 0 默认，1 调色板，2 true color）
    u32 bg
    u16 attrs      与 term.Attr 一致：bit0 bold, bit1 dim, bit2 italic,
                   bit3 underline, bit4 blink, bit5 inverse, bit6 hidden,
                   bit7 strike, bit8 wide head, bit9 wide tail
    u8  width      1 或 2
    u8  flags      bit0 wide head, bit1 wide tail
```

`sinceVersion` 与终端版本相同返回 0，Swift 跳过重绘；缓冲区不足返回负的所需字节数。Swift 只缓存 `version` 变化的快照。

### 4.4 远程桌面帧格式（小端）

```
header 24 bytes:
  u64 version      帧状态版本
  i32 width, height  远端桌面像素尺寸
  u32 rectCount    本次包含的脏矩形数
  u32 reserved

rectCount × 16 bytes:
  i32 x, y, width, height

随后按 rectCount 顺序，每个矩形逐行紧排 BGRA 像素（行跨度 = width×4）
```

- `sinceVersion == 0` 或没有累积脏矩形时返回整屏；否则只返回尚未交给前端的脏矩形。
- 快照成功写出后才清空脏矩形；缓冲区不足时不清空，前端用返回的所需大小重试。
- 大于 2732×2048 的远端画面被拒绝，会话以明确错误结束，避免分配上百 MB 的双缓冲。

## 5. 终端子系统

### 5.1 真源

`term.Terminal` 是唯一终端状态（与桌面一致），负责 VT 解析、滚动历史、选择、超链接、图片协议。Swift 只做渲染与输入。

### 5.2 渲染

- `UIView.draw(_:)` + CoreText 绘制 cell 网格，首版用 CoreGraphics，后续可换 Metal。
- 等宽字体：SF Mono / Menlo，按 cell 宽度绘制；宽字符占两格。
- 颜色、粗体、斜体、下划线、反显、光标、选区。
- 滚动：手指位移按像素跟手，每帧向核心提交最新的绝对行偏移（`sessions.scrollTo`），
  快照与目标行之间的零头作为绘制平移，因此不会逐行跳动；抬手后带减速惯性，靠近
  两端时有阻尼回弹。滚动期间 CADisplayLink 提升到屏幕最大刷新率（ProMotion 120 Hz，
  `Info.plist` 里的 `CADisableMinimumFrameDurationOnPhone` 必须为 true，否则 iPhone
  会被限制在 60 Hz），空闲时降到 60 Hz。背景色按同色连续段合并填充，减少绘制调用。拖动由自定义
  `TerminalScrollPanGestureRecognizer` 识别：手指移动约 1 pt 即进入 `.began`
  （系统 `UIPanGestureRecognizer` 的触摸容差约 10 pt），并把这 1 pt 计入位移；
  不等待长按识别失败，因此滑动既没有 0.35 秒延迟，也没有起步死区。
  `.began`/`.changed` 里直接 `setNeedsDisplay()` 并提交最新的绝对行偏移，
  不必等下一次 CADisplayLink 回调，慢速起手的第一帧也不会落后于手指。长按仍
  用于扩展选区。
- 复制：长按进入选区，拖动扩展，点浮动复制按钮或键盘条 Copy 复制选中单元格；未选中时复制整屏。
- 链接：`term.LinksAt`。
- 键盘与尺寸：终端只忽略底部容器安全区（`.ignoresSafeArea(.container, edges: .bottom)`），
  SwiftUI 的键盘 inset 仍然生效（含 84 pt 输入条），整个视图随键盘上移。绘制视图的
  `contentMode = .top`，UIKit 在 bounds 连续变化时先平移旧位图，不必逐帧全量重绘；
  pty 的列/行在布局稳定 120 ms 后才提交 `sessions.resize`，避免键盘动画每一帧都触发
  SIGWINCH 与整屏重排。pty 行数按 `bounds.height - (safeAreaInsets.bottom + 4)` 计算，
  键盘收起时最后一行不会被 home indicator 遮住。

### 5.3 输入

- 软键盘文本直接 UTF-8 发送到 `sessions.input`。
- 快捷键条固定两行 84 pt（`TerminalInputBar`）。下排是输入命令时常用的 Esc、Tab、
  Ctrl-C/D/Z、方向键与粘贴/复制；上排在没有建议时显示 Home/End、PgUp/PgDn 和
  readline 的 Ctrl-A/E/K/U/W/R/L。UIKit 只在配件首次展示时读取高度，所以这里不改
  高度，只交换上排内容，键盘不会跳动。
- 内联补全：输入停顿 0.22 秒后，`completion.complete` 读取屏幕上的当前命令行，
  依次合并精选参数组合（`internal/completion/combos.go`）、本机历史与内置目录，
  描述按界面语言本地化（`internal/completion/localize.go`）。上排变成可横向滚动的
  建议芯片（命令、描述、将插入的后缀），点按即把后缀发送到 shell；整行恰好是某个
  组合时不再有候选，上排恢复为按键。
- 硬件键盘：`UIKeyCommand` 映射到 `term` 的键盘编码（复用桌面键位规则）。
- 中文 IME：`UITextInput` 组合输入，预编辑文本叠加显示，提交后发送。

### 5.4 会话保持与关闭

- 终端右上角的下箭头只是收起界面，SSH 连接、终端状态、SFTP worker 与端口转发都
  继续运行；`sshconn.KeepAliveConfig{Interval: 30s, Timeout: 10s}` 持续发送
  keepalive。App 进入后台仍会被 iOS 挂起，回到前台后若连接已断开可重连。
- 主机列表分为“配置主机”和“Open sessions”两部分。点按配置主机每次都会新建一个
  终端，因此同一服务器可以开多个 shell；已连接的终端单独列出，点按某个会话即可
  回到它。同一主机的多个会话按 Terminal 1 / Terminal 2 编号区分。
- 关闭连接在 Open sessions 中长按或左滑对应会话；主机行的菜单可以一次关闭该主机的
  全部连接。关闭后 SSH 连接、SFTP worker 与端口转发一并结束。

### 5.5 SFTP 文本编辑

- 目录中的文件不再默认下载：点按文本文件打开 `RemoteTextEditorView`，由
  `files.openText` 读取远端内容。只接受 UTF-8、非符号链接、大小不超过 8 MiB 的普通
  文件；二进制或超限文件显示说明，并提供明确的 Download 操作。
- 工具栏菜单的 `New file` 在当前目录调用 `files.create` 新建空文件（O_EXCL，拒绝
  覆盖已有路径），成功后立即用编辑器打开；`New folder` 仍是独立的目录操作。
- 编辑器使用 `UITextView`，关闭智能引号、智能破折号、自动更正与自动大写，避免破坏
  配置语法；保存通过 `files.saveText` 原子替换（目录不可写时回退为原地写入），保留
  文件权限与 BOM。
- 保存携带打开时的 SHA-256 revision；远端内容已变化时返回 `conflict`，不会静默覆盖，
  用户可重新加载后再保存。未保存修改在关闭或下拉时要求确认。
- 下载仍可从文件菜单、编辑器菜单或二进制回退页触发，并进入传输中心。

### 5.6 远程桌面认证与代理

- 主机模型补齐桌面字段：`protocol`（ssh/vnc/rdp）、`viaSSH`、`clipboard`、`console`、
  `scale` 与 `proxy`。VNC 允许空用户名；默认端口 VNC 5900、RDP 3389。
- VNC 密码与 RDP 用户名/密码保存在 vault（`host:<catalogID>:password`，与 SSH 密码
  同一个键）。连接时先静默使用已保存的凭据；服务器拒绝后删除旧密码并弹出提示。
  提示中勾选“记住密码”时，只有认证成功后才写入 vault。
- 出站代理与桌面版同构：`socks5` / `http`，可选用户名、密码与 bypass 列表。代理密码
  存在 `host-proxy:<catalogID>:<hash>:password`，与桌面备份/同步互通；SSH 会话在
  `sshconn.Config.Proxy` 中拨号，VNC/RDP 直连时使用同一个 `proxyDialer`。
- `viaSSH` 指向的 SSH 会话已连接时，桌面流量走该连接的 `DialContext`，不单独拨号；
  隧道断开则关闭桌面会话。

## 6. AI 子系统

### 6.1 复用

- `internal/ai/llm`：OpenAI Chat Completions、Responses、Anthropic Messages、SSE 流。
- `internal/ai/agent`：ReAct 循环、工具调用、事件。
- `internal/ai/tools` + `internal/ai/gate`：工具定义、风险审批。
- `internal/ai/store`：provider、模型、对话历史（SQLite）。
- `internal/ai/mcp`：MCP over HTTP/SSE。
- `internal/ai/risk`、`internal/ai/redact`：风险与脱敏。

### 6.2 新增

`internal/mobilecore`：

- `ai_run.go`：run 生命周期、事件流、取消、审批；`agent.Run` 直接复用。
- `ai_settings.go`：支持同时配置多个供应商（`providers[]` + `currentId`），每个供应商
  有自己的协议、端点、key、模型、模式与系统提示；列表与当前选择存
  `<DataDir>/ai.json`（0600），API key 只存 vault（`aikey:<provider id>`）。切换供应商
  只改 `currentId`，其余供应商的 key 保留。旧版单供应商 `ai.json`（扁平字段）在首次
  启动时折叠为 id 等于协议名的供应商，因此旧 vault 键无需迁移。删除供应商时同时删除
  它的 vault key。设置页可以新增、编辑、左滑删除并在“当前供应商”选择器中切换。
- `ai_backend.go`：`gate.Backend` 实现。`Sessions` / `ReadTerminal` / `Exec` 已接入；
  `Exec` 复用 `internal/ai/capture` 向 shell 注入包装命令并解析结束标记，与桌面一致。
  SFTP 工具随 M3 接入。
- `Approver`：发 `ai.approval` 事件，等待 `ai.approve`；支持“本次 run 内同工具同级别自动允许”。
- 事件：`ai.state`、`ai.text`、`ai.reasoning`、`ai.tool`、`ai.toolResult`、
  `ai.approval`、`ai.usage`、`ai.error`、`ai.modeRequest`。
- 审批策略：`risk.Policy{Enabled:true, AutoUpTo:Low}` + `gate.ModeAsk`；读操作自动，
  Medium/High 弹窗，Critical 需输入 `CONFIRM`。
- 对话按**主机**保存到 `ai.db`（复用 `internal/ai/store` 的 SQLite schema）。同一主机
  重新连接、关闭终端标签或重启 App 后，助手继续使用原来的上下文；面板打开时自动
  载入当前对话。`ai.reset` 只开始新对话，旧对话仍保留在历史列表中，可重新打开继续。
- 消息归属通过 `ToolJSON` 中的 `ios_host` 标记区分，不修改桌面共享 schema。
- 助手回复由 `MarkdownMessageView` 渲染：标题、段落、粗斜体、行内代码、代码块（可复制）、
  有序/无序列表、引用、分隔线、管道表格与链接，流式输出时同样按块解析。
- 对话按主机隔离：不同主机的历史互不相通；同一主机的多个终端共享该主机的当前对话。
- 工具参数与工具结果默认折叠到可读行数，提供“显示更多/收起”查看完整内容，不再使用
  无法展开的省略号；工具结果不再嵌套在固定高度的小滚动区里。
- 打开助手时自动定位到底部（包括刚载入历史后的首次布局）；流式输出期间持续跟随。

### 6.3 iOS 裁剪

- 移除 `internal/ai/cli`（外部 CLI provider）。
- MCP 仅保留 HTTP/SSE；stdio 配置在导入时忽略并提示。
- API key 存 `vault` + Keychain。
- AI 网络请求由 Go 发出，后台挂起时中断；回到前台可恢复会话历史。

## 7. 主机、凭据与存储

数据目录为 `Application Support/Motty/`，与桌面共享同一套数据格式：

| 文件 | 内容 |
| --- | --- |
| `config.db` | configstore（SQLite，format 23）；主机记录 kind `host`，payload `{"o": ord, "d": savedHost}`；可读入桌面 0.3.6 的 format 25 备份（`maxBackupFormat`） |
| `vault.json` | 加密 secrets；数据密钥存 Keychain（`kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly`） |
| `known_hosts` | OpenSSH known_hosts（`internal/knownhosts`） |
| `session-logs.db` / `history.db` | 会话日志、命令历史（SQLite） |
| `ai.json` | 供应商列表（协议、端点、模型、模式）与当前供应商；API key 不在此文件 |
| `ai.db` | 按主机保存的助手对话历史（`internal/ai/store` schema） |
| `sync.json` | 同步目标、ETag、lastSync；同步凭据不在此文件 |
| `exports/` | 最近 5 个 `.mottybak` 导出 |
| `inbox/` | 从 Files 导入、待恢复的备份文件 |

- 主机 ID 使用桌面确定性 `catalogIDFor`（address/user/port[/protocol] 的 SHA-256，`host_` + 24 hex），同一服务器在 iOS 与桌面合并为一条。
- `savedHost` 的全部桌面字段往返保留；保存时对既有 record 做 map 补丁，未知字段不丢。
- vault 键：`sshkey:<hex>`（值 `{"name","key"}`）、`passphrase:<sshkey ref>`、`aikey:<provider id>`、`host:<catalogID>:password|otp`、`host-proxy:<catalogID>:<hash>:password`、`webdav:password`、`s3:secret`、`gist:token`、`backup:password`。
- 私钥可从 Files 导入，也可直接粘贴 OpenSSH/PEM 文本；两种方式都写入 `vault.json`，与桌面版同样加密。主机记录的 `identity` 有三种来源：本应用写入的 `sshkey:` vault 引用、桌面同步主机的私钥指纹（`SHA256:...`）、SSH config 导入的文件路径。建连时 `keyStore.resolveIdentity` 先按指纹、再按路径的文件名匹配已保存的 key，统一换成 vault 引用；匹配不到的原样保留。Automatic 模式先试选中的 key 再试全部已保存 key，Key 模式没有选中项时也试全部已保存 key。
- 主机密码只存 vault：编辑主机时可输入/删除；主机保存为私钥或 Agent 认证时自动删除旧密码；删除主机同时删除其 password/otp。连接首次出现密码提示且 vault 有对应密码时自动提交，服务器拒绝后删除该密码并改为询问用户；用户在提示中勾选“保存密码”时，仅在认证成功后写入 vault。
- 启动迁移幂等：旧 `hosts.json` → config.db（ID 改为 catalog ID，冲突加后缀）；旧 `keys/` 文件 → vault；`ai.json` 中的明文 API key → vault。旧文件保留，可回退。
- 备份：`backup.Seal`（Argon2id + AES-256-GCM，envelope v2，密码 ≥8 字符），文档含 records（含 tombstone）、known_hosts、secrets，排除同步凭据自身。恢复前生成 `config.db.before-restore`。
- 桌面发布线（0.3.6）的文档 format 已到 25，而本树 `configFormat` 仍为 23：iOS 写入与上传仍标 23；自动同步可读入至 `maxBackupFormat = 25`，更高的 epoch 以 `ErrTooNew` 拒绝；**手动恢复（从服务器或本地文件）不检查文档 epoch**，任何版本的文档都直接应用，因为内部记录原样保存、host 保存时对既有 payload 做字段补丁，新 epoch 增加的字段不会丢失。
- 同步：下载远端文件 → `backup.Open` → configstore/vault/known_hosts 合并 → 只有本地更新时才 `Seal` + `Put`；ETag/条件写，`ErrChanged` 最多重试 4 次；tombstone 保留 90 天；数据变化 5 秒防抖，失败指数退避 30 秒 → 30 分钟。
- Swift 端 `scenePhase == .active` 时补一次同步（自动模式）；同步期间用 `beginBackgroundTask` 争取短暂后台时间。
- 文件导入导出：`UIDocumentPicker` / Share Sheet；SFTP 下载到沙盒 `downloads/`，可导出到 Files。

## 8. iOS 平台约束

| 约束 | 影响 | 应对 |
| --- | --- | --- |
| 后台挂起 | SSH 断开 | keepalive + 自动重连；回到前台恢复；提示用户 |
| 不能 fork/exec | 无本地 shell/CLI | 移除相关入口 |
| App Store 禁止自更新 | 无自更新 | 移除 |
| 沙盒 | 路径变化 | `Application Support` / `Documents` |
| 无 X server | X11 转发不可用 | 移除入口 |
| 内存限制 | 大 scrollback/图片 | 默认 2,000 行，可设置 |
| 触摸 | 无悬停/右键 | 长按菜单、双指滚动、手势 |
| 审核 | 加密出口声明 | App Store Connect 填写；SSH 为豁免范围 |

### 8.1 多语言

- 与桌面版一致，提供 English、简体中文、繁體中文三种界面语言。
- 全部界面文案放在 `ios/Motty/Localizable.xcstrings`（String Catalog）；SwiftUI 按环境 locale 查找，静态 `Text`/`Label`/`Section` 等无需额外处理。
- 设置页的 Language 可选“跟随系统”或固定一种语言，选择存 `@AppStorage("motty.language")`；应用根视图用 `.environment(\.locale, …)` 应用。
- RPC 回调等非 SwiftUI 上下文构造的提示用 `AppLanguage.localized` 即时解析。
- Go 核心返回的错误、服务器输出与命令内容保持原样，不做翻译。

## 9. 里程碑

| 阶段 | 内容 | 状态 |
| --- | --- | --- |
| M0 | Go c-archive + Swift 桥接 + 终端渲染骨架 | 已编译通过；待真机验证 |
| M1 | 主机 CRUD/分组/导入、认证、known_hosts、软键盘快捷键、滚动/复制、重连 | 主要功能完成；多标签/known_hosts 管理界面待做 |
| M2 | AI 助手：provider/模型、对话、流式、工具调用、审批 | 核心闭环完成；对话按主机持久化到 ai.db，历史列表/继续/删除已完成；MCP/附件待做 |
| M3 | SFTP 浏览/传输、文件 App、分享 | 完成；Go 单测通过，真机待完整验证 |
| M4 | 端口转发、会话日志、命令历史、补全 | 完成；Go 单测通过，真机待验证 |
| M5 | VNC/RDP | 完成；Go 单测通过，真实服务器待验证 |
| M6 | 数据迁移、Keychain、云同步、备份/恢复 | 完成；Go 单测通过，云服务与桌面互导待验证 |
| M7 | 三语界面（English/简体中文/繁體中文），跟随系统 + 设置切换 | 完成；模拟器验证三语切换，真机已安装 |

当前构建方式：

```sh
./ios/scripts/build-go.sh          # 生成 ios/Frameworks/MottyCore.xcframework
open ios/Motty.xcodeproj           # 在 Xcode 里设置签名后运行
```

命令行验证（无需签名）：

```sh
DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer \
xcodebuild -project ios/Motty.xcodeproj -target Motty \
  -configuration Debug -sdk iphonesimulator build CODE_SIGNING_ALLOWED=NO
```

## 10. 风险

1. **终端 IME 与键盘**：iOS 中文输入法在终端里的组合输入需要专门验证。
2. **后台断线**：无法像桌面常驻；需要在产品层面接受。
3. **完整功能的工作量**：UI 重写约 3~5 万行 Swift；建议严格按里程碑推进。
4. **VNC/RDP 触摸交互**：桌面鼠标语义需要重新设计（指针模式/直接触摸）。
5. **AI 工具审批**：移动端弹窗打断体验，需要批处理与“本次会话允许”。
