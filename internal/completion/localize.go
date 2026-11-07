package completion

import "strings"

// The UI languages command descriptions are translated into. The catalog is
// English (it comes from Carapace); this table covers the commands, options and
// curated combinations a phone user meets most. Anything missing falls back to
// the catalog's English text rather than disappearing.
const (
	LocaleEnglish = "en"
	LocaleHans    = "zh-Hans"
	LocaleHant    = "zh-Hant"
)

type translation struct {
	hans string
	hant string
}

// translations is keyed by the exact English description in the catalog (or in
// combos.go), so one entry serves every command that repeats the wording.
var translations = map[string]translation{
	// Commands.
	"the stupid content tracker":                                     {"Git 版本控制工具", "Git 版本控制工具"},
	"A self-sufficient runtime for containers":                       {"容器运行时", "容器執行環境"},
	"kubectl controls the Kubernetes cluster manager":                {"控制 Kubernetes 集群", "控制 Kubernetes 叢集"},
	"copy files and directories":                                     {"复制文件和目录", "複製檔案和目錄"},
	"move (rename) files":                                            {"移动（重命名）文件", "移動（重新命名）檔案"},
	"remove files or directories":                                    {"删除文件或目录", "刪除檔案或目錄"},
	"make directories":                                               {"创建目录", "建立目錄"},
	"change file mode bits":                                          {"修改文件权限", "修改檔案權限"},
	"change file owner and group":                                    {"修改文件所有者和组", "修改檔案擁有者和群組"},
	"estimate file space usage":                                      {"估算文件占用空间", "估算檔案佔用空間"},
	"report file system disk space usage":                            {"查看文件系统磁盘空间", "查看檔案系統磁碟空間"},
	"Display amount of free and used memory in the system":           {"显示系统内存使用情况", "顯示系統記憶體使用情況"},
	"report a snapshot of the current processes":                     {"查看当前进程快照", "查看目前程序快照"},
	"print lines that match patterns":                                {"输出匹配模式的行", "輸出符合模式的行"},
	"search for files in a directory hierarchy":                      {"在目录树中查找文件", "在目錄樹中尋找檔案"},
	"tar - an archiving utility":                                     {"归档工具", "封存工具"},
	"transfer a URL":                                                 {"传输 URL", "傳輸 URL"},
	"a non-interactive network retriever":                            {"非交互式下载工具", "非互動式下載工具"},
	"OpenSSH remote login client":                                    {"OpenSSH 远程登录客户端", "OpenSSH 遠端登入用戶端"},
	"OpenSSH secure file copy":                                       {"OpenSSH 安全文件复制", "OpenSSH 安全檔案複製"},
	"Query or send control commands to the system manager":           {"查询或控制系统服务", "查詢或控制系統服務"},
	"Query the journal":                                              {"查询系统日志", "查詢系統日誌"},
	"output the last part of files":                                  {"输出文件末尾", "輸出檔案末尾"},
	"output the first part of files":                                 {"输出文件开头", "輸出檔案開頭"},
	"the package manager for JavaScript":                             {"JavaScript 包管理器", "JavaScript 套件管理器"},
	"Go is a tool for managing Go source code":                       {"Go 源代码管理工具", "Go 原始碼管理工具"},
	"package manager for Python packages":                            {"Python 包管理器", "Python 套件管理器"},
	"Rust's package manager":                                         {"Rust 包管理器", "Rust 套件管理器"},
	"GNU make utility to maintain groups of programs":                {"GNU 构建工具", "GNU 建置工具"},
	"stream editor for filtering and transforming text":              {"流式文本编辑与转换", "串流文字編輯與轉換"},
	"pattern scanning and processing language":                       {"模式扫描与处理语言", "模式掃描與處理語言"},
	"a fast, versatile, remote (and local) file-copying tool":        {"快速多功能的远程/本地复制工具", "快速多功能的遠端/本機複製工具"},
	"Send a signal to a process":                                     {"向进程发送信号", "向程序傳送訊號"},
	"send ICMP ECHO_REQUEST to network hosts":                        {"向主机发送 ICMP 回显请求", "向主機傳送 ICMP 回顯請求"},
	"DNS lookup utility":                                             {"DNS 查询工具", "DNS 查詢工具"},
	"OpenSSL command line program":                                   {"OpenSSL 命令行工具", "OpenSSL 命令列工具"},
	"Vi IMproved, a programmer's text editor":                        {"Vi 增强版文本编辑器", "Vi 增強版文字編輯器"},
	"terminal multiplexer":                                           {"终端复用器", "終端多工器"},
	"mount a filesystem":                                             {"挂载文件系统", "掛載檔案系統"},
	"create a new user or update default new user information":       {"创建用户或更新默认信息", "建立使用者或更新預設資訊"},
	"apt is a commandline package manager":                           {"apt 命令行包管理器", "apt 命令列套件管理器"},
	"The missing package manager for macOS":                          {"macOS 包管理器", "macOS 套件管理器"},
	"server-side JavaScript runtime":                                 {"服务端 JavaScript 运行时", "伺服器端 JavaScript 執行環境"},
	"Yarn is a package manager that doubles down as project manager": {"Yarn 包管理器", "Yarn 套件管理器"},
	"Fast, disk space efficient package manager":                     {"快速、节省磁盘的包管理器", "快速、節省磁碟的套件管理器"},
	"compiler for the Rust programming language":                     {"Rust 编译器", "Rust 編譯器"},
	"Launches a Java application":                                    {"启动 Java 应用", "啟動 Java 應用程式"},

	// Options.
	"change files and directories recursively":                                                {"递归修改文件和目录", "遞迴修改檔案和目錄"},
	"output a diagnostic for every file processed":                                            {"为每个处理的文件输出诊断信息", "為每個處理的檔案輸出診斷資訊"},
	"same as -dR --preserve=all":                                                              {"等同于 -dR --preserve=all", "等同於 -dR --preserve=all"},
	"preserve the specified attributes":                                                       {"保留指定属性", "保留指定屬性"},
	"copy directories recursively":                                                            {"递归复制目录", "遞迴複製目錄"},
	"explain what is being done":                                                              {"显示正在执行的操作", "顯示正在執行的操作"},
	"HTTP POST data":                                                                          {"以 HTTP POST 发送数据", "以 HTTP POST 傳送資料"},
	"Fail fast with no output on HTTP errors":                                                 {"HTTP 错误时立即失败且不输出", "HTTP 錯誤時立即失敗且不輸出"},
	"Show document info only":                                                                 {"仅显示响应头", "僅顯示回應標頭"},
	"Pass custom header(s) to server":                                                         {"向服务器发送自定义请求头", "向伺服器傳送自訂標頭"},
	"Follow redirects":                                                                        {"跟随重定向", "跟隨重新導向"},
	"Write to file instead of stdout":                                                         {"写入文件而非标准输出", "寫入檔案而非標準輸出"},
	"Specify request method to use":                                                           {"指定请求方法", "指定請求方法"},
	"Show error even when -s is used":                                                         {"即使静默也显示错误", "即使靜默也顯示錯誤"},
	"Silent mode":                                                                             {"静默模式", "靜默模式"},
	"print sizes in powers of 1024 (e.g., 1023M)":                                             {"以 1024 进制显示大小", "以 1024 進位顯示大小"},
	"list inode information instead of block usage":                                           {"显示 inode 信息", "顯示 inode 資訊"},
	"print file system type":                                                                  {"显示文件系统类型", "顯示檔案系統類型"},
	"write counts for all files, not just directories":                                        {"统计所有文件而非仅目录", "統計所有檔案而非僅目錄"},
	"print sizes in human readable format (e.g., 1K 234M 2G)":                                 {"以易读格式显示大小", "以易讀格式顯示大小"},
	"print the total for a directory (or file, with --all)":                                   {"显示目录（或文件）总计", "顯示目錄（或檔案）總計"},
	"display only a total for each argument":                                                  {"仅显示每个参数的总计", "僅顯示每個參數的總計"},
	"PATTERNS are extended regular expressions":                                               {"模式按扩展正则表达式解释", "模式按擴充正規表示式解讀"},
	"print only names of FILEs with selected lines":                                           {"仅输出包含匹配行的文件名", "僅輸出包含符合行的檔案名稱"},
	"ignore case distinctions in patterns and data":                                           {"忽略大小写", "忽略大小寫"},
	"search only files that match GLOB (a file pattern)":                                      {"仅搜索匹配 GLOB 的文件", "僅搜尋符合 GLOB 的檔案"},
	"select non-matching lines":                                                               {"选择不匹配的行", "選擇不符合的行"},
	"print line number with output lines":                                                     {"输出行号", "輸出行號"},
	"like --directories=recurse":                                                              {"递归搜索目录", "遞迴搜尋目錄"},
	"Follow the journal":                                                                      {"持续跟踪日志", "持續跟隨日誌"},
	"Number of journal entries to show":                                                       {"显示的日志条数", "顯示的日誌筆數"},
	"Show entries with the specified priority":                                                {"按优先级筛选日志", "依優先順序篩選日誌"},
	"Show entries not older than the specified date":                                          {"显示该时间之后的日志", "顯示該時間之後的日誌"},
	"Show logs from the specified unit":                                                       {"显示指定单元的日志", "顯示指定單元的日誌"},
	"Show entries not newer than the specified date":                                          {"显示该时间之前的日志", "顯示該時間之前的日誌"},
	"The name of the kubeconfig context to use":                                               {"使用的 kubeconfig 上下文", "使用的 kubeconfig 內容"},
	"If present, the namespace scope for this CLI request":                                    {"本次请求的命名空间", "本次請求的命名空間"},
	"no error if existing, make parent directories as needed":                                 {"已存在时不报错，并按需创建父目录", "已存在時不報錯，並視需要建立父目錄"},
	"print a message for each created directory":                                              {"为每个创建的目录输出信息", "為每個建立的目錄輸出資訊"},
	"Select by effective user ID (EUID) or name":                                              {"按有效用户 ID 或名称选择", "依有效使用者 ID 或名稱選擇"},
	"ignore nonexistent files and arguments, never prompt":                                    {"忽略不存在的文件且不提示", "忽略不存在的檔案且不提示"},
	"prompt according to WHEN: never, once (-I), or always (-i); without WHEN, prompt always": {"按 WHEN 提示：never、once (-I) 或 always (-i)", "依 WHEN 提示：never、once (-I) 或 always (-i)"},
	"remove directories and their contents recursively":                                       {"递归删除目录及其内容", "遞迴刪除目錄及其內容"},
	"Show all properties/all units currently in memory, including dead/empty ones":            {"显示所有单元（含已停用）", "顯示所有單元（含已停用）"},
	"Shortcut for --state=failed":                                                             {"--state=failed 的快捷方式", "--state=failed 的快捷方式"},
	"Do not pipe output into a pager":                                                         {"不通过分页器输出", "不透過分頁器輸出"},
	"Start or stop unit after enabling or disabling it":                                       {"启用或停用后立即启动或停止", "啟用或停用後立即啟動或停止"},
	"Connect to user service manager":                                                         {"连接用户级服务管理器", "連線使用者級服務管理器"},
	"output appended data as the file grows":                                                  {"持续输出文件新增内容", "持續輸出檔案新增內容"},
	"output the last NUM lines, instead of the last 10":                                       {"输出最后 NUM 行", "輸出最後 NUM 行"},
	"create a new archive":                                                                    {"创建归档", "建立封存"},
	"extract files from an archive":                                                           {"从归档中提取文件", "從封存中擷取檔案"},
	"use archive file or device ARCHIVE":                                                      {"使用指定的归档文件", "使用指定的封存檔案"},
	"filter the archive through gzip":                                                         {"通过 gzip 压缩", "透過 gzip 壓縮"},
	"list the contents of an archive":                                                         {"列出归档内容", "列出封存內容"},
	"verbosely list files processed":                                                          {"详细列出处理的文件", "詳細列出處理的檔案"},

	// Curated combinations (combos.go).
	"Short status with the branch":                  {"带分支的简短状态", "含分支的簡短狀態"},
	"Compact commit graph":                          {"紧凑的提交图", "精簡的提交圖"},
	"Changed files with line counts":                {"改动文件及行数统计", "變更檔案及行數統計"},
	"What is staged":                                {"已暂存的改动", "已暫存的變更"},
	"Stage every change":                            {"暂存所有改动", "暫存所有變更"},
	"Stage changes hunk by hunk":                    {"按块暂存改动", "依區塊暫存變更"},
	"Stage tracked changes and commit":              {"暂存已跟踪改动并提交", "暫存已追蹤變更並提交"},
	"Add to the last commit, keep its message":      {"追加到上次提交并保留说明", "追加到上次提交並保留說明"},
	"Push and track the current branch":             {"推送并跟踪当前分支", "推送並追蹤目前分支"},
	"Pull, rebasing local commits":                  {"拉取并变基本地提交", "拉取並重訂本地提交"},
	"Fetch every remote and drop dead branches":     {"拉取所有远端并清理失效分支", "拉取所有遠端並清理失效分支"},
	"Create and switch to a branch":                 {"创建并切换到分支", "建立並切換到分支"},
	"Switch back to the previous branch":            {"切回上一个分支", "切回上一個分支"},
	"Branches with their upstream":                  {"分支及其上游", "分支及其上游"},
	"Stash everything, untracked files included":    {"暂存全部改动（含未跟踪文件）", "暫存全部變更（含未追蹤檔案）"},
	"Restore the last stash":                        {"恢复最近的暂存", "還原最近的暫存"},
	"Unstage a file":                                {"取消暂存文件", "取消暫存檔案"},
	"Undo the last commit, keep its changes":        {"撤销上次提交并保留改动", "復原上次提交並保留變更"},
	"Rewrite the last commits":                      {"改写最近的提交", "改寫最近的提交"},
	"The last commit with changed files":            {"上次提交及改动文件", "上次提交及變更檔案"},
	"Remote repositories":                           {"远端仓库", "遠端儲存庫"},
	"List tags":                                     {"列出标签", "列出標籤"},
	"Who changed these lines":                       {"查看这些行的作者", "查看這些行的作者"},
	"Every container, running or not":               {"所有容器（含已停止）", "所有容器（含已停止）"},
	"Running containers":                            {"运行中的容器", "執行中的容器"},
	"Local images":                                  {"本地镜像", "本機映像"},
	"Follow a container's last 100 lines":           {"跟踪容器最后 100 行日志", "跟隨容器最後 100 行日誌"},
	"Open a shell in a running container":           {"在运行中的容器里打开 Shell", "在執行中的容器裡開啟 Shell"},
	"Run an interactive container, remove it after": {"运行交互式容器，结束后删除", "執行互動式容器，結束後刪除"},
	"Start the stack in the background":             {"后台启动编排栈", "在背景啟動編排堆疊"},
	"Stop and remove the stack":                     {"停止并移除编排栈", "停止並移除編排堆疊"},
	"Follow the stack's logs":                       {"跟踪编排栈日志", "跟隨編排堆疊日誌"},
	"The stack's containers":                        {"编排栈的容器", "編排堆疊的容器"},
	"Delete every unused image, network and cache":  {"删除所有未使用的镜像、网络和缓存", "刪除所有未使用的映像、網路和快取"},
	"Pods with node and address":                    {"Pod 及其节点和地址", "Pod 及其節點和位址"},
	"Pods in every namespace":                       {"所有命名空间的 Pod", "所有命名空間的 Pod"},
	"Services in every namespace":                   {"所有命名空间的 Service", "所有命名空間的 Service"},
	"Nodes with address and version":                {"节点及其地址和版本", "節點及其位址和版本"},
	"Follow a pod's log":                            {"跟踪 Pod 日志", "跟隨 Pod 日誌"},
	"Everything about a pod":                        {"查看 Pod 的完整信息", "查看 Pod 的完整資訊"},
	"Apply a manifest":                              {"应用清单文件", "套用清單檔案"},
	"Delete a pod":                                  {"删除 Pod", "刪除 Pod"},
	"Run a shell in a pod":                          {"在 Pod 中运行 Shell", "在 Pod 中執行 Shell"},
	"Kubeconfig contexts":                           {"kubeconfig 上下文列表", "kubeconfig 內容列表"},
	"Switch context":                                {"切换上下文", "切換內容"},
	"Restart a deployment":                          {"重启 Deployment", "重新啟動 Deployment"},
	"Forward a service port locally":                {"将 Service 端口转发到本地", "將 Service 連接埠轉送到本機"},
	"Long listing with sizes and hidden files":      {"长格式列出大小和隐藏文件", "長格式列出大小和隱藏檔案"},
	"Newest files first":                            {"按时间倒序排列", "依時間倒序排列"},
	"Copy a directory":                              {"复制目录", "複製目錄"},
	"Move and say what moved":                       {"移动并显示过程", "移動並顯示過程"},
	"Delete a directory and its contents":           {"删除目录及其内容", "刪除目錄及其內容"},
	"Create parents as needed":                      {"按需创建父目录", "視需要建立父目錄"},
	"Make a file executable":                        {"赋予文件可执行权限", "賦予檔案可執行權限"},
	"Change ownership recursively":                  {"递归修改所有者", "遞迴修改擁有者"},
	"Size of every entry here":                      {"当前目录各项的大小", "目前目錄各項的大小"},
	"Free space per filesystem":                     {"各文件系统的可用空间", "各檔案系統的可用空間"},
	"Memory in use":                                 {"内存使用情况", "記憶體使用情況"},
	"Search this tree with line numbers":            {"在此目录树中搜索并显示行号", "在此目錄樹中搜尋並顯示行號"},
	"Search this tree, ignoring case":               {"在此目录树中忽略大小写搜索", "在此目錄樹中忽略大小寫搜尋"},
	"Find by name below this directory":             {"在此目录下按名称查找", "在此目錄下依名稱尋找"},
	"Find files by name":                            {"按名称查找文件", "依名稱尋找檔案"},
	"Extract a .tar.gz":                             {"解压 .tar.gz", "解壓縮 .tar.gz"},
	"Create a .tar.gz":                              {"创建 .tar.gz", "建立 .tar.gz"},
	"Download quietly, failing on HTTP errors":      {"静默下载，HTTP 出错时失败", "靜默下載，HTTP 出錯時失敗"},
	"Show response headers":                         {"显示响应头", "顯示回應標頭"},
	"Resume an interrupted download":                {"断点续传下载", "續傳下載"},
	"Sync over SSH, compressed":                     {"通过 SSH 压缩同步", "透過 SSH 壓縮同步"},
	"Connect on another port":                       {"使用其他端口连接", "使用其他連接埠連線"},
	"Connect with a private key":                    {"使用私钥连接", "使用私密金鑰連線"},
	"Forward a remote port locally":                 {"将远端端口转发到本地", "將遠端連接埠轉送到本機"},
	"Copy a directory over SSH":                     {"通过 SSH 复制目录", "透過 SSH 複製目錄"},
	"A unit's state":                                {"查看单元状态", "查看單元狀態"},
	"Restart a unit":                                {"重启单元", "重新啟動單元"},
	"Follow one unit's journal":                     {"跟踪某个单元的日志", "跟隨某個單元的日誌"},
	"Recent errors from the journal":                {"查看最近的错误日志", "查看最近的錯誤日誌"},
	"Follow a growing file":                         {"跟踪文件新增内容", "跟隨檔案新增內容"},
	"Install dependencies":                          {"安装依赖", "安裝相依套件"},
	"Run the development script":                    {"运行开发脚本", "執行開發腳本"},
	"Build for production":                          {"生产环境构建", "正式環境建置"},
	"Run the test script":                           {"运行测试脚本", "執行測試腳本"},
	"Test every package":                            {"测试所有包", "測試所有套件"},
	"Run this package":                              {"运行当前包", "執行目前套件"},
	"Build every package":                           {"构建所有包", "建置所有套件"},
	"Add missing and drop unused modules":           {"添加缺失模块并移除无用模块", "加入缺少的模組並移除無用模組"},
	"Vet every package":                             {"检查所有包", "檢查所有套件"},
	"Create a virtual environment":                  {"创建虚拟环境", "建立虛擬環境"},
	"Install the pinned requirements":               {"安装锁定版本的依赖", "安裝鎖定版本的相依套件"},

	// Sources of a candidate, not catalog text.
	"From your history": {"来自你的命令历史", "來自你的命令歷史"},
}

// LocalizeDesc returns desc in locale, or desc itself when this build has no
// translation for it.
func LocalizeDesc(desc, locale string) string {
	if desc == "" {
		return ""
	}
	entry, ok := translations[desc]
	if !ok {
		return desc
	}
	switch NormalizeLocale(locale) {
	case LocaleHans:
		return entry.hans
	case LocaleHant:
		return entry.hant
	default:
		return desc
	}
}

// NormalizeLocale maps a BCP-47 tag to one of the three description
// languages. Anything that is not Chinese reads English.
func NormalizeLocale(locale string) string {
	lower := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(locale), "_", "-"))
	switch {
	case strings.HasPrefix(lower, "zh-hant"), strings.HasPrefix(lower, "zh-tw"),
		strings.HasPrefix(lower, "zh-hk"), strings.HasPrefix(lower, "zh-mo"):
		return LocaleHant
	case strings.HasPrefix(lower, "zh"):
		return LocaleHans
	default:
		return LocaleEnglish
	}
}
