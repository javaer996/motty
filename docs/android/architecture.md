# Motty Android 移植架构

方案 B：Go 核心 + 原生 Kotlin/Compose。Android 与 iOS 共用同一套 Go 核心，通过 JNI 暴露同一组 C ABI。Android 额外提供本地 shell。

## 1. 目标与范围

| 能力 | Android | 说明 |
| --- | --- | --- |
| SSH 终端（多会话） | ✅ | 复用 `internal/sshconn` + `internal/term` |
| 主机管理/分组/标签/导入 | ✅ | 复用 `internal/mobilecore` 的 `hosts.*` |
| 密码/私钥/跳板机/代理 | ✅ | 复用 `internal/sshconn`、`internal/vault` |
| 本地 shell | ✅ | 复用 `internal/localshell` + `creack/pty`，系统 `/system/bin/sh` |
| SFTP 浏览/上传/下载 | ✅ | 复用 `internal/sftpclient`（SAF 中转） |
| 端口转发 | ✅ | 复用 `internal/portforward` |
| 会话日志/命令历史/补全 | ✅ | 复用 `internal/sessionlog`、`internal/cmdhistory`、`internal/completion` |
| AI 助手 | ✅ | 复用 `internal/ai/*` |
| VNC / RDP | ✅ | 复用 `internal/rfb`、`internal/rdpcore` |
| 云同步/备份 | ✅ | 复用 `internal/mobilecore/sync.go`、`backup.go` |
| 串口 / X11 / 应用内自更新 / AI CLI / MCP stdio | ❌ | 与 iOS 相同的平台限制 |

最低 Android 8.0（API 26），compileSdk/targetSdk 35，arm64-v8a 为 M1 唯一打包 ABI。

## 2. 总体架构

```
┌──────────────────────────────────────────────────┐
│ Jetpack Compose App                               │
│  Hosts / Terminal / Keys / Settings / AI / SFTP   │
│  TerminalView (android.view.View, 自绘)           │
├──────────────────────────────────────────────────┤
│ Kotlin 核心层                                      │
│  CoreClient · CoreRepository · ViewModels         │
│  MottyNative (JNI 声明) · VaultKeyStore           │
├──────────────────────────────────────────────────┤
│ libmotty.so (Go, -buildmode=c-shared)             │
│  mobile/              C ABI + JNI shim            │
│  internal/mobilecore  平台无关的应用服务            │
│  internal/*           现有纯 Go 能力               │
└──────────────────────────────────────────────────┘
```

### 2.1 为什么不用 gomobile

与 iOS 相同：导出面大、高频二进制终端流不适合回调、C ABI 稳定、避免 `golang.org/x/mobile` 版本风险。Android 用 `-buildmode=c-shared` 生成 `.so`，JNI shim 作为包内 C 文件编入同一个库。

### 2.2 线程模型

- Compose 主线程只做渲染与输入。
- 控制面：`CoreClient` 的单线程执行器串行调用 `start` / `call` / `stop`，非幂等 RPC 不会并发执行。
- 事件轮询：`Dispatchers.IO` 上的常驻协程；前台服务存活期间持续轮询。
- 屏幕快照：`TerminalView` 的 `Choreographer` 帧回调直接调用。
- Go 侧会话状态由 `session.mu` 保护；快照在 JNI 调用内拷入 direct buffer，绘制阶段不再持有 Go 锁。

## 3. Go 侧目录

```
mobile/
  main_android.go          android && cgo：C ABI 与 App 单例
  main_android_nocgo.go    android && !cgo：空 main，保证 go build ./mobile 可用
  jni_android.c            JNI shim（包内 C 文件，编入 libmotty.so）
  main_ios.go              iOS c-archive（不变）
  main_other.go            !ios && !android：桌面空 main

internal/mobilecore/
  app.go                   Config{DataDir, Version, LocalShell, VaultKey}；sessions.openLocal
  session.go               shellBackend 接口；本地会话生命周期；pump 最终 drain
  local_supported.go       !ios：本地 shell 实现（internal/localshell）
  local_ios.go             ios：unavailable stub，不引入 localshell
  keystore_android.go      android：拒绝隐式文件密钥
  keystore_other.go        !ios && !android：文件 keystore（桌面测试）
  store.go                 openDataStoreWithKey
  logs.go                  本地会话 ProtocolLocal

internal/vault/
  static_keystore.go       Android 内存 keystore（Kotlin 已解包的主密钥）
```

## 4. 桥接协议

### 4.1 C ABI

RPC 载荷使用字节而非 `jstring`：JNI modified UTF-8 会破坏补充平面字符（emoji）。

```c
void *MottyStartAlloc(const void *dataDir, int dataDirLen,
                      const void *version, int versionLen,
                      const void *vaultKey, int vaultKeyLen, int *outLen);
void *MottyCallAlloc(const void *method, int methodLen,
                     const void *params, int paramsLen, int *outLen);
void  MottyFreeBuffer(void *ptr);

int MottyPollEventBytes(void *buf, int cap);
int MottyScreenSnapshotBytes(unsigned int sessionID, unsigned long long sinceVersion,
                             void *buf, int cap);
int MottyDesktopFrameBytes(unsigned int desktopID, unsigned long long sinceVersion,
                           void *buf, int cap);
void MottyStop(void);
```

- `*Alloc` 返回 `C.CBytes` 分配的 UTF-8 JSON，调用方用 `MottyFreeBuffer` 释放。一次分配返回结果，保证 `sessions.open` / `hosts.save` / `ai.send` 等非幂等 RPC 不会因重试执行两次。
- 缓冲区方法：正数 = 写入字节数；0 = 无变化/无事件；负数 = 所需容量；快照未知会话 -1。只读、可安全重试。
- 上限：method ≤ 256 字节、params ≤ 16 MiB、start 字符串 ≤ 1 MiB、结果 ≤ 64 MiB；拒绝负长度。
- 返回统一 JSON envelope：`{"ok":true,"result":{...}}` / `{"ok":false,"error":{"code":"...","message":"..."}}`。

### 4.2 JNI shim

`jni_android.c` include `_cgo_export.h`（cgo 在编译包内 C 文件前生成；`libmotty.h` 在链接期才生成，不能用于包内 C 文件）。

符号命名对应 Kotlin `dev.motty.app.core.MottyNative` 的实例方法：

```
Java_dev_motty_app_core_MottyNative_start
Java_dev_motty_app_core_MottyNative_call
Java_dev_motty_app_core_MottyNative_pollEvent
Java_dev_motty_app_core_MottyNative_screenSnapshot
Java_dev_motty_app_core_MottyNative_desktopFrame
Java_dev_motty_app_core_MottyNative_stop
```

- `byte[]` 用 `GetByteArrayRegion` / `SetByteArrayRegion`；空数组传 `NULL, 0`。
- 快照/事件用 direct `ByteBuffer`：`GetDirectBufferAddress` + `GetDirectBufferCapacity`；非 direct 返回 -2。
- `jlong` 会话/桌面 ID 转 `uint32_t`；Kotlin 模型仍用 `Long`。
- 参数非法时返回静态错误 envelope。
- 若 c-shared 未导出 `Java_...` 符号，回退方案：`app/src/main/cpp/motty_jni.c` 用 CMake 编成独立 `libmotty_jni.so`，Gradle 提供 `jni.h` 并正常导出 JNI 符号。

### 4.3 屏幕快照格式（小端）

```
header 48 bytes:
  u64 version
  u32 cols, rows
  u32 cursorX, cursorY
  u32 cursorStyle   0 block, 1 underline, 2 bar
  u32 flags         bit0 alt screen, bit1 cursor visible, bit2 cursor steady,
                    bit3 bracketed paste, bit4 app cursor keys, bit5 reverse video,
                    bit6 mouse, bit7 synchronized output
  u32 scrollback
  u32 viewOffset
  u32 reserved0, reserved1（编码器写 0）

每行 rows 次：
  u32 cellCount
  cellCount × 16 bytes:
    u32 rune
    u32 fg        kind<<24 | value（kind 0 默认，1 调色板，2 true color）
    u32 bg
    u16 attrs     bit0 bold, bit1 dim, bit2 italic, bit3 underline, bit4 blink,
                  bit5 inverse, bit6 hidden, bit7 strike, bit8 wide head, bit9 wide tail
    u8  width     1 或 2
    u8  flags     bit0 wide head, bit1 wide tail
```

- 快照只含当前视图行（已应用 `viewOffset` 的回滚位置），不回传全部历史。
- 线格式只有每格基础 rune；组合字符与 OSC 8 超链接不可渲染/复制，这是 iOS 与 Android 共有的限制。
- 桌面帧格式与 iOS 相同：24 字节头 + 脏矩形表 + BGRA 像素。

## 5. 本地 shell

### 5.1 会话接入

`session.shell` 从 `*sshconn.Shell` 抽象为接口：

```go
type shellBackend interface {
    Drain(dst []byte) []byte
    Write(p []byte) bool
    Resize(cols, rows int)
    Done() <-chan struct{}
    Err() error
    Close()
    Kind() shellkind.Kind
}
```

`*sshconn.Shell` 与 `*localshell.Shell` 都直接满足，无需新传输层。新增 RPC：

```json
{"method":"sessions.openLocal","params":{"cols":120,"rows":30}}
```

- `Config.LocalShell == false` 时返回 `unavailable`（iOS 不启用）。
- 合成 Host：`Label: "Local Shell"`，跳过 `Host.Validate`，日志用 `ProtocolLocal` 与 `TargetUnknown`。
- `SessionInfo` 增加 `"local": true`（省略时 iOS 解码忽略）。

### 5.2 启动参数（Android）

| 项 | 值 |
| --- | --- |
| Shell | `/system/bin/sh`（目标真机为 mksh R59） |
| Args | `[]string{}`（显式非 nil，避免 localshell 默认加 `-l`） |
| Dir | `<DataDir>/home`（`Start` 时创建，0700） |
| HOME | `<DataDir>/home` |
| TMPDIR | `<DataDir>/tmp` |
| SHELL | `/system/bin/sh` |
| PATH | `/system/bin:/system/xbin:/vendor/bin:/product/bin` |
| USER / LOGNAME | `android` |
| LANG | `C.UTF-8` |

`TERM=xterm-256color` 与 `COLORTERM=truecolor` 由 `localshell` 追加。非 Android 测试机保留继承的 `PATH`。

### 5.3 生命周期

- `connectLocal` 成功后 `phase=connected`、记录 `connectedAt`、启动 `pump` 与 `watchLocal`。
- `watchLocal` 等待 `shell.Done()` 或会话 `done`，记录退出错误、`phase=closed`、`markDone`；会话保留（可重连）。
- `reconnect` 按 `s.local` 选择 `connectLocal` 或 `connect`。
- `pump` 在 `<-done` 关闭后再 drain 一次，避免丢失退出前的最后输出（同时修复 SSH 侧潜在竞态）。
- 本地进程以应用 UID 运行，受 SELinux 限制；不做 root。

## 6. Keystore

- vault 主密钥（32 字节）由 Kotlin 生成并保存在 Android Keystore 包装中；Go 不直接调用 Keystore。
- `VaultKeyStore`：alias `motty.vault.wrap`，AES-256-GCM；包装文件 `files/motty/vault.keywrap`：`"MTKY" | version=1 | IV(12) | ciphertext+tag(48)`；AAD `motty.vault.key.v1`；原子写入，0600。
- 启动流程：解包 → 传给 `MottyStartAlloc` → Go 复制后清零输入 → Kotlin 立即清零本地数组。
- Go `vault.StaticKeystore`：空密钥时 `Load` 返回 `ErrKeystoreNotFound`、`Store` 返回 `ErrKeystoreUnavailable`，避免创建下次无法打开的 vault。
- Keystore 密钥丢失且 `vault.json` 存在时：显示阻断错误，绝不静默重建。`android:allowBackup="false"`，因为设备绑定的 Keystore 密钥不会随备份迁移。
- 旧的 `vault.key` 文件（开发构建）首次启动迁移进 Keystore 包装后删除。

## 7. 终端子系统

### 7.1 渲染

- 自定义 `android.view.View`，`Choreographer` 帧循环，仅在附着且页面可见时运行。
- direct `ByteBuffer` 容量 `48 + rows*4 + cols*rows*16`，不足时按负数所需容量扩容（上限 16 MiB）。
- 复用 Paint、基础类型数组与行偏移表；背景同色合并 `drawRect`，文本按样式合并 `drawText`。
- 字体：等宽 13sp；`cellWidth = measureText("M")`（不取整）；宽字符占两格。
- 颜色：标准 xterm 16 色 + 6×6×6 色立方 + 24 级灰阶；默认前景 `#E0E0E0`、背景 `#0D0E13`。
- 光标样式 0/1/2 对应 block/underline/bar；尊重 cursor visible 标志。

### 7.2 输入

- `onCheckIsTextEditor() = true`，自定义 `BaseInputConnection`：`commitText` 把 `\n` 转 `\r`，`deleteSurroundingText` 发送 `0x7F`，预编辑文本本地叠加绘制。
- `EditorInfo` 使用 `TYPE_TEXT_VARIATION_VISIBLE_PASSWORD | NO_SUGGESTIONS`，禁用自动纠错但保留中文 IME 组合输入。
- 硬件键映射与 `internal/term/keys.go` 一致：方向/Home/End 按 app cursor keys 选择 `ESC O` 或 `ESC [`；Ctrl+A..Z → 0x01..0x1A；Alt 前缀 ESC；F1–F12/Insert/Delete/PageUp/PageDown 标准序列。
- 键栏（Compose）提供 Esc、Tab、Ctrl-C/D/Z、方向、Home/End、PgUp/PgDn、readline 快捷键、复制/粘贴。

### 7.3 滚动、选择、剪贴板

- 拖动按像素跟手，向核心提交最新绝对行偏移（`sessions.scrollTo`）；会聚 Channel 保证不排队过期位置；抬手后 fling + 边缘回弹。
- 备用屏（flags bit0）无历史，禁用滚动。
- 长按选词、拖动扩展；复制优先选区，否则整屏；粘贴在 bracketed paste 模式包裹 `ESC[200~`/`ESC[201~`。

### 7.4 尺寸

- 初始 PTY 80×24；`onSizeChanged` 后 120ms 防抖提交 `sessions.resize`，只在尺寸变化时发送。
- 旋转/IME 动画不会造成 resize 风暴。

## 8. 应用层

- `MottyApplication` 持有唯一 `CoreRepository`；Go 拥有全部持久化，Android 无数据库。
- 事件：`session.state` 更新会话表；`session.prompt` 记录待答问题；`sessions.close` 不推事件，前端成功后本地移除；回到前台用 `sessions.list` 权威对账。
- 前台服务：`dataSync` 类型，LOW 通知；有活动/认证中会话时运行；`onTimeout`（Android 15）停止并提示；应用 `allowBackup=false`。
- 密钥导入走 SAF `OpenDocument`，拒绝 `.pub` 与 >1 MiB 文件；base64 传给 `keys.import`。
- 语言：English / 简体中文 / 繁體中文 + 跟随系统；`AppCompatDelegate.setApplicationLocales` + `res/xml/locales_config.xml`。
- Go 返回的错误不翻译。

## 9. 里程碑

| 阶段 | 内容 | 状态 |
| --- | --- | --- |
| M1 | 骨架、主机/密钥、SSH 终端、本地 shell、认证提示、重连、前台服务、主题/图标/三语 | 进行中 |
| M1.1 | 终端打磨：分数滚动、选中手柄、光标闪烁、响铃、字号、Keystore 恢复 | 待做 |
| M2 | AI 助手 + 隐私保护 | 待做 |
| M3 | SFTP 浏览/传输（SAF 中转） | 待做 |
| M4 | 端口转发、会话日志、命令历史、补全 | 待做 |
| M5 | VNC / RDP | 待做 |
| M6 | 云同步与备份（SAF 导入导出） | 待做 |

## 10. 构建

```sh
# Go 原生库（需要 NDK）
ANDROID_HOME=/usr/local/share/android-commandlinetools \
  ANDROID_NDK_HOME=$ANDROID_HOME/ndk/27.2.12479018 \
  android/scripts/build-go.sh

# APK
cd android && ./gradlew :app:assembleDebug
adb install -r app/build/outputs/apk/debug/app-debug.apk
```

产物：`android/app/src/main/jniLibs/arm64-v8a/libmotty.so`（不提交）。

## 11. 风险与已知限制

1. JNI 符号导出依赖 c-shared 的 `-rdynamic`；T4 用 `llvm-nm -D` 验证，失败回退 CMake shim。
2. 16 KB 页对齐需显式链接参数；测试机为 4096 页，只能做静态校验。
3. 进程死亡会结束所有会话；前台服务与 OEM 白名单只能降低概率。
4. Android 15 `dataSync` 前台服务有 6 小时/24 小时配额。
5. 快照不含组合字符、超链接、图片协议；M1 接受。
6. Keystore 密钥丢失不可恢复，只能清除机密（M1.1 提供入口）。
7. OPPO/ColorOS 的电池管理可能杀死进程；真机验收包含后台存活测试。
