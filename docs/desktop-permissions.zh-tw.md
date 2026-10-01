# Desktop 反覆要求桌面資料夾權限：調查方式

HYCLV-202，調查基底：`84cc0f1340eb934cec1472ae5b6b698420ff9a38`。
這是原始碼調查，尚未取得回報者 Mac 上的提示、程序工作目錄或簽章證據，
不能據此宣稱已確認根因或已修復。

macOS 的 Files and Folders 權限涵蓋 Desktop、Documents、Downloads 等位置。
官方說明：[Apple Platform Security](https://support.apple.com/en-ph/guide/security/secddd1d86a6/web)。

## 已確認的呼叫鏈

| 路徑 | 程式證據 | 需要核對的現場條件 |
| --- | --- | --- |
| Desktop 啟動 daemon → 第三方 CLI 探測 | `apps/desktop/src/main/daemon-manager.ts` 的 `startDaemon` 使用 `execFile`，未指定 cwd；`server/cmd/multica/cmd_daemon.go` 的 daemon child 未指定 Dir；`server/pkg/agent/launch.go` 的版本探測繼承 cwd | App／daemon 的 cwd 是否在 Desktop；實際第三方 CLI 是否讀取該目錄 |
| 重複探測 | `server/internal/daemon/agents_refresh.go`：availability 每 2 分鐘，已註冊 CLI version 每 10 分鐘；`agents_probe.go` 的 login-shell fallback cache 為 30 分鐘 | 提示時間是否對應探測週期或啟動；快取不是保證每 30 分鐘執行 |
| shell 啟動檔 | `server/internal/daemon/config.go` 的 executable 查找使用 shell `-ilc` | 使用者 shell rc 是否有讀取桌面的命令 |
| 本機專案 | `apps/desktop/src/main/local-directory.ts` 驗證選定路徑；`server/internal/daemon/local_directory.go` 對目錄 ReadDir 並建立暫存檔測試 | 選定路徑或其 symlink 是否指向 Desktop |
| local skills | `server/internal/daemon/local_skills.go` 的列舉流程會跟隨 symlink | skill roots 或其中的 symlink 是否指向 Desktop |

一般 workdir 預設為 `$HOME/multica_workspaces_<profile>`，不在 Desktop；
`MULTICA_WORKSPACES_ROOT` 或 explicit override 可以改變它。
Desktop 的 `DEFAULT_PREFS.autoStart=false`，所以不能說所有 App 啟動都必然啟動 daemon。

## 簽章假設需要分開驗證

`apps/desktop/electron-builder.zh-tw.yml` 固定 appId 為
`ai.multica.desktop.zh-tw.local`，設定 `notarize: false`，未固定 Developer ID identity。
`apps/desktop/scripts/package.mjs` 說明停用 identity auto discovery 時使用 ad-hoc。
這不代表目前使用者安裝的 App 一定是 ad-hoc；未公證也不直接證明權限會反覆詢問。

在使用者指定 Mac 上，比較實際 App 的安裝路徑、版本與下列唯讀結果，
不要拿來源 repository 版本代替已安裝版本：

```sh
codesign -dv --verbose=4 '/實際/Multica 繁中版.app'
codesign -dr - '/實際/Multica 繁中版.app'
```

同時記錄提示是開啟／更新 App 時、背景閒置時，還是選本機專案／開技能時出現，
以及允許後是否重複。只收集相關程序 cwd、路徑與對應時間的事件；不回傳完整
環境變數、私密 shell 設定或完整日誌。

若 cwd 繼承確定是原因，候選修正為 daemon／背景探測指定非受保護的工作目錄，
保留任務自己的 `opts.Cwd`，並以 fake CLI 驗證 cwd。不先擴大為所有檔案存取權限，
也不以重設整個權限資料庫取代根因調查。
