# Agent CLI 與模型清單自動更新

HYCLV-159 的功能層，基底為 `zh-tw` 的
`e26c9f821644672501bfded8397077eecc660a15`。這份文件描述候選程式行為，
不代表已部署。此功能不需要資料庫 migration，也不會修改 Agent 已選的模型。

## 自動執行流程

- daemon 完成初次註冊後，在背景嘗試更新此 daemon 已偵測到的內建 CLI。
  Desktop 管理的 daemon 同樣適用。
- 之後依**執行主機的本地時區**，每天 00:00／12:00 嘗試一次；排程每分鐘檢查，
  並非從啟動時間起算每 12 小時。主機停機期間不執行，重啟時補做一次。
- 有執行中的工作、領取中的工作或 Multica 自身更新時，延後至下一分鐘檢查。
  取得既有 claim barrier 後暫停新工作領取，heartbeat 繼續運作。
- 每個 CLI 最多更新 5 分鐘，逐一執行；個別失敗不妨礙其他 CLI。
  不執行 sudo、不安裝不存在的 provider、不輸出可能含認證資訊的安裝器輸出。
  安裝器失敗後等下一個排程時段再試。
- 更新後重新偵測實際 CLI 版本並沿用 runtime 註冊回報；不能只憑安裝器退出成功
  就宣稱新版已可執行。daemon 結束時會取消並等待更新子程序回收。

`MULTICA_AGENT_AUTO_UPDATE=false` 可停用 CLI 自動更新。預設啟用，
與控制 Multica binary 的 `MULTICA_DAEMON_AUTO_UPDATE`／`MULTICA_DAEMON_AUTO_RELOAD`
分開；不會把自架繁中版 Multica 換成公開版。

## 支援範圍與權限

目前更新器針對 Codex 與 Claude，依**實際執行檔的安裝來源**選擇更新方式：

| 安裝方式 | 行為 |
| --- | --- |
| 全域 npm | 核對 package.json、宣告的 bin 與實際檔案，明確指定原 prefix 更新既有套件 |
| Homebrew | 核對 Cellar／Caskroom 與安裝紀錄，使用該 prefix 的 brew 更新對應 formula／cask |
| Claude 原生版 | 核對使用者的版本目錄及穩定入口，再執行官方 `claude update` |
| Codex 獨立版 | 核對 standalone 安裝中繼資料、current 及穩定入口，再執行官方 `codex update` |
| Windows、未知 wrapper、自訂 runtime profile、其他 provider | 不猜測更新方式；CLI 更新不支援，模型探索仍沿用既有 provider 能力 |

更新需要 daemon 執行帳號可寫入既有安裝目錄。root 管理的全域 CLI 不會自動提權，
須由管理者另行核准可維護的安裝方式。原生／套件管理器更新仍受其網路、版本釘選、
release channel 與權限規則影響，並不保證所有機器立即取得相同版本。

同一使用者的多個 daemon／Desktop 共用 `~/.multica/agent-cli-update.lock`，
OS 鎖避免安裝器同時替換檔案，程序結束即釋放。具備此功能的 daemon 在領取工作前持有
共享鎖，直到該批所有工作結束；安裝器需要獨佔鎖，因此另一個新版 daemon 執行工作時
也會延後更新。舊版 daemon、其他帳號與終端機自行啟動的 CLI 不遵循此鎖。
共用安裝且仍有這些執行來源時，需指定維護窗口，或停用自動更新。

## Server 模型清單

Server 在 daemon 註冊及 heartbeat 時，透過既有 model-list 佇列主動要求探索。
每個 runtime 最多每 10 分鐘發起一次自動嘗試；Redis 部署透過具 TTL 的原子預約
避免多個 Server 重複排程。失敗或空結果同樣受間隔限制，不會每次 heartbeat 都重試。
離線 runtime 在重新連線後恢復探索。版本更新到新清單通常最多相差一個 10 分鐘週期，
另加 heartbeat、排隊與探索時間。

daemon 收到探索工作時會略過自身 60 秒 memo，使用該 runtime 的 CLI 路徑及 launch prefix。
Codex 優先讀取 `debug models` 的有效目錄；上限 15 秒，失敗時最多花 5 秒讀 bundled
備援，總探索期限 20 秒。Claude 沿用動態 `list_models`；新 ID 由 provider 回報，
不在 Multica 中硬編碼新版本的模型名稱。

隱藏或 CLI 宣告不可用的模型不會列成可選模型。Codex bundled／static 結果標記為備援，
不覆蓋 Server 最近一次成功清單，也不更新該成功清單的觀測時間。
不同 runtime 分開保存；Web 與 Desktop 沿用相同 Server API，毋須新增選單。
「CLI 回報的有效目錄」不保證供應商剛剛連線成功，也不代表逐一模型已通過推論測試。

## 診斷與驗證

daemon 的本地 `/health` 新增 `agent_maintenance`：enabled、state、last_attempt_at、
next_attempt_at，以及每個 provider 的 completed／failed／unsupported_installation。
completed 指該輪已結束；個別 provider 仍可能失敗，實際版本以註冊回報為準。
詳細原因由 daemon 日誌提供，安裝器原始輸出不記錄。

使用假 CLI 測試，不讓預設測試碰到真實 CLI 或登入帳號：

```sh
cd server
../scripts/go-test-with-agent-cli-guard.sh -- go test -race ./internal/daemon
../scripts/go-test-with-agent-cli-guard.sh -- go test -p 2 -parallel 2 ./pkg/agent -run 'Test.*Codex.*'
# Handler 測試必須另設隔離的 DATABASE_URL；Redis 測試另設 REDIS_TEST_URL。
../scripts/go-test-with-agent-cli-guard.sh -- go test ./internal/handler -run 'Test.*ModelCatalog|TestHeartbeatDiscoversModelsWithoutPicker'
```

Handler 的 TestMain 在 DB 不可用時會跳過測試並退出成功，必須確認有實際執行測試，
不能只看 exit code。不要把正式 DB 或 Redis 提供給測試。

## 繁中版重建與發布

重建時保留 daemon 的 `agent_maintenance*`／`agent_cli_update*`、config／health／Run 接點，
以及模型探索的 memo invalidation、Codex fallback 身分、Server refresh reservation 與
register／heartbeat 接點。保留 Unix／Windows build tags；Windows 不猜測安裝方式。
既有 claim barrier、runtime 版本回報、模型佇列及 Server 成功快取是依賴的共用契約。

Server 需要此版本才會主動要求探索；daemon 需要此版本才有 CLI 排程及有效 Codex 目錄。
Desktop 使用內附 daemon，必須在後續 Desktop 成品一併帶入 daemon 修改。
只更新 Server 無法替換各機器上的 daemon。發布前仍須依
[Server 交接程序](server-handoff.zh-tw.md) 核准與驗證，不能直接用此文件作為部署授權。
停用排程不會降版已更新的第三方 CLI；恢復舊 CLI 需另外保存／安裝其對應版本。

更新器契約查核來源（2026-09-24）：
[Codex 官方更新實作](https://github.com/openai/codex/blob/main/codex-rs/tui/src/update_action.rs)、
[Claude 官方安裝與更新文件](https://code.claude.com/docs/en/setup)。
套件管理器入口與 standalone metadata 仍需在未來 CLI 安裝格式變更時重新查核。
