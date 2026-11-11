# Daemon 維護閘門與換版

> HYC-26 已改為 App 統管配套，以及無 App 的跨機更新。最新入口、交易驗收及兩版保留見 [配套更新維護](unified-update.zh-tw.md)。本文件保留底層 gate 與舊版首次導入機制。下方獨立釘選方案不再作為新的 Mac 部署方式。

本文件說明支援維護閘門的 daemon 換版前提與首次導入方式。正式變更必須先核准機台、範圍、影響、驗證與回滾；此文件本身不構成授權。以實際發布品的 commit、版本、雜湊及來源為準，不能只憑版本字串認定含有閘門。

## 維護閘門

支援的 daemon 依 CLI profile 解析目錄，在其中檢查 `daemon.maintenance` 目錄：預設 profile 在 `~/.multica/daemon.maintenance`，具名 profile 在 `~/.multica/profiles/<profile>/daemon.maintenance`；使用 task-local config root 時依該 root 的 profile 配置。精確路徑應以該 daemon `/health` 的 `maintenance_gate.path` 為準，且確認同機、同 profile、同 daemon ID。可見路徑不含密碼；不要分享設定或憑證內容。

外部 updater 原子建立目錄並放入 `owner` JSON 檔 作所有權紀錄；daemon 不建立、不移除、不依時間自動到期。目錄跨 daemon 重啟仍保留，因此新版尚未解除前仍禁止領取工作。daemon 在每次領取、嘗試維護屏障與伺服器觸發更新時，在同一把鎖內讀取目錄；目錄存在、型別不正確或檢查失敗都會阻擋新工作及更新。不得由健康檢查建立或解除閘門。同 UID 的本機程序屬信任邊界，須避免其他管理工具同時改動該目錄。

`/health` 的 `maintenance_gate` 包含 `supported`、`path`、`blocked`、`ready`、`claims_in_flight`。只有目錄是可存取的正確目錄，且進行中的領取和 active task 均為零、沒有更新或既有維護屏障時，`ready` 才會是 true。等待中的領取轉交給任務時，daemon 先計入 active 再解除 in-flight，因此不把交接空隙當作排空。檔案、符號連結或存取錯誤會 blocked 但不 ready；目錄不存在則不 blocked 也不 ready。`active_task_count=0`、HTTP 200 或 `blocked=true` 單獨都不表示可換版。不要以 timer 自行解除殘留目錄。

實作位置：`server/internal/daemon/maintenance_gate.go`、`daemon.go` 的 claim／update 屏障、`health.go`；profile 路徑解析見 `server/internal/cli/config.go`。這個閘門只保護能辨識它的 daemon；不會攔截舊 daemon、其他 profile、外部寫檔或任意外部服務重啟。

## 常態更新

常態流程只適用於**執行中的舊版與將切換的候選版都支援此閘門**，且操作權限、服務管理方式、固定版資產和回滾方式均已確認。要更新正在執行自己的 daemon 時，由其他實體機器或人操作；先驗證操作者的主機與目標，不能從被更新 daemon 的任務中重啟自己。

1. 確認 profile、daemon ID、服務管理者與 MainPID 相符，記錄 runtime 清單、版本、commit、執行檔 SHA-256、binary／launcher／CA／config／state 路徑。驗證固定版本來源、簽章或雜湊與 server API 相容；不要輸出秘密。
2. 使用同版本 updater，核對其 `--help` 的介面及所有權行為，在正確 profile 建立 gate。已存在的 gate 不得覆蓋或逕自接管；先確認建立者與狀態。
3. 輪詢該 daemon 健康資訊，確認 `supported=true`、`blocked=true`、`ready=true`、`claims_in_flight=0`，並核對 daemon ID、profile 與版本。等待中的任務應自然完成；不要強制中止以換取 ready。
4. 保持 gate，依受核准方式更新並重啟**確切**的服務或 daemon，保留原 profile 和身分。新程序必須從相同 gate 路徑讀取同一閘門；核對新執行檔 hash、commit、ID、runtime 註冊與心跳，確認新 daemon 的 gate 仍 blocked 且 ready。若 preflight 等待 server，待 server 恢復後驗證，不得因此提前移除 gate。
5. 驗證通過後，由持有者依 updater 的正式介面解除其 gate，確認 `blocked=false`、runtime 上線，並按核准驗收計畫檢查派工。逐機執行，保留跨機控制端。舊版不回報 gate capability 時必須拒絕常態 updater，不能用 active=0 替代。

## Linux updater 介面

`scripts/deploy/update-daemon.sh` 轉交同目錄的 Python 3 工具，只接受 Linux user systemd 的單一、獨占安裝。必須在目標擁有者的正常遠端 shell 或人工環境執行；工具會拒絕 daemon task 身分與工作目錄標記。不要移除 task 環境或改借擁有者憑證繞過限制。

原生 binary 的核准範本如下；大寫值必須換成核准包的實際內容。`OBSERVER_CLI` 是獨立且固定的可用 CLI，不能是即將替換的檔案。`EVIDENCE_DIR` 必須是新目錄，其父目錄已存在。資產與入口路徑須為絕對路徑且不可經 symlink。既有 `.multica`／profile state 連結可保留：工具解析為擁有者持有的實體目錄，固定其 inode，在每個關鍵步驟核對連結仍指向相同目錄，直接在該實體目錄維護 gate；不遷移或改寫 state 連結。

```sh
scripts/deploy/update-daemon.sh "$PROFILE" "$UNIT" "$STAGED_BINARY" "$BINARY_SHA256" \
  --layout binary --cli "$OBSERVER_CLI" --health-url "http://127.0.0.1:$HEALTH_PORT/health" \
  --version "$TARGET_VERSION" --commit "$APPROVED_FULL_COMMIT" \
  --evidence-dir "$EVIDENCE_DIR" --no-shared-profiles
```

launcher 模式將第三個位置參數改為已離線準備的完整版本目錄中的 `launcher`，使用 `--layout launcher --entrypoint "$INSTALLED_LAUNCHER" --binary "$STAGED_BINARY"`；其餘參數相同，SHA-256 始終指真正 binary。支援發行 installer 的 hardlink launcher 形態，原、新完整版本目錄都保留；工具只原子切換 launcher，不執行網路 installer、不裁剪舊版或改 CA。單一安裝的所有 profile／unit、間接參照與其他使用者須先盤清；兩模式均要求 `--no-shared-profiles` 表明已確認沒有共用者。共用安裝須另外設計所有 profile 的協調屏障，不可用此旗標略過。

舊、新 binary 的 `version --output json` 必須提供完整 `build_commit` 與 `daemon_maintenance_gate="v1"`；執行中的 health 亦須回報 capability 與相同完整 commit。先驗離線資產 SHA、版本及能力，再建立 gate。工具持有獨立 flock、gate inode 與 owner；備份完成並重新核對 MainPID 後才停止精確 unit。更新與回滾都核對 `/proc/<pid>/exe` 雜湊、profile、原 daemon_id、全部 workspace/runtime 集合，並從新版註冊後的新基準觀察至少三次 last_seen 前進。預設每 15 秒取樣、每次等待上限 180 秒；`--heartbeat-samples` 不得少於 3。整個窗口固定 PID，解除前再次檢查。

`updated` 代表更新與驗證完成；`rolled_back_verified` 代表更新失敗但回滾及驗證完成，仍以非零碼退出。`blocked` 表示自動恢復未能安全完成，保留 gate 供核准後處置。心跳證據在 `heartbeats.jsonl`，基準、原入口備份及結果另存 evidence 目錄，不包含登入設定。任何解除後的 fsync／signal 錯誤會明報 `*_gate_released_error`、`claims_may_resume=true`；此時工作可能已恢復，不能直接停止或再回滾，須重新取得屏障並排空。保留 gate 不代表正式服務健康；故障時需持續處理。

## 舊版首次導入：Server 持續運作

先實測舊版的能力。缺少 gate 的舊版仍拒絕常態 updater。
首次導入使用明確的專用入口。中央 Server 持續運作。
維護交界可能漏接或中斷任務。據實記錄，交由使用者重新驅動。
不要承諾自動補跑，也不要把 `active_task_count=0` 當成排他屏障。

1. 核對目標。通過條件：跨實體機操作者、原精確服務、MainPID、profile、daemon ID 與全部 runtime 都符合核准盤點。
2. 核對舊資產。通過條件：實跑映像 SHA-256 與核准 baseline 相符。舊版缺完整 build metadata 時，不補造 commit。
3. 等待已知工作。通過條件：已盡量等候目前已知工作完成，並記錄無 gate 的競態及可能中斷。資料無法核對時停止。
4. 備份原形態。通過條件：完整舊 App、CLI、真正入口 wrapper／launcher、權限及配套均可恢復。不要假定版本目錄中存在 launcher。
5. 建立新版 gate。通過條件：正確 profile 的 gate 路徑、owner 與 inode 已記錄。這個 gate 不保護舊版。
6. 停止目標。通過條件：只停止已核對的原 unit 或精確程序。中央 Server、其他 daemon 與 profile 繼續運作。
7. 換入候選。通過條件：候選來源、完整 commit、配套及 hash 通過。從原管理方式啟動，不另起游離 daemon。
8. 驗證新版。通過條件：gate 跨重啟仍 blocked 且 ready；原身分及執行版本正確；全部原 runtime 至少跨三個心跳週期。
9. 恢復接單。通過條件：由 gate 持有人解除後，完成一次已授權的派工、執行及回傳。沒有派工證據時維持待驗收。

故障時只停止已證明屬於本交易的候選，恢復整組舊資產及原入口。
回到無 gate 舊版時，不能宣稱 gate 保護有效。
中央 Server 仍持續運作，舊版可能立即領取工作。
核對原身分、實跑 hash、心跳與實際派工，並記錄漏接或中斷。
使用者重新驅動受影響任務。再次維護仍走明確的舊版專用流程。

## 錯誤與復原

- gate 已存在：先辨識所有權、是否仍有 updater 或維護在途；不要覆蓋 `owner` JSON 檔、因逾時刪除或讓兩個操作者共同持有。接手與解除須依核准程序及 updater 介面處理。
- `blocked=true` 但 `ready=false`：檢查 in-flight、active、更新與其他維護屏障，確認 gate 是可存取的目錄及路徑正確；持續觀察至工作結束，存取錯誤、異常型別或不明所有權時停止並查明。
- 維護中 profile state 連結或目錄被改動：停止自動處理並明報 `profile_changed=true`、`claims_may_resume=true`。舊實體 gate 留存不保證 daemon 仍停派；先重新核對身分與實際 state，再取得可證明的屏障，不能直接停止或回滾。
- 新程序未起、ID 不同、runtime 離線、hash 不符或健康資訊缺少 capability：保留 gate，停止發布並依已核准回滾處理。若回退版具 gate，仍須驗證其 gate 和身分；若不具 gate，採上述舊版專用回復流程，記錄可能漏接。
- 解除後確認恢復健康與派工。未完成驗證前不要刪除備份或把失敗視為成功。

## macOS Desktop：既有獨立釘選安裝

本節只說明既有獨立釘選安裝的辨識與保護。新導入由 App 統管 App、內附 CLI 及 daemon，不再新增獨立 daemon。已有獨立標記時只回報並停止，不自行移除或接管。

既有安裝的 [macOS 安裝、遷移、升級、回滾與驗收程序](macos-pinned-daemon.zh-tw.md) 不含內部主機、註冊 UUID 或憑證。原機器清單不作為公開操作範本；每次操作另備受限存取的核准包，以實際盤點為準。

Desktop 透過 profile 的 `daemon-management.json` 與 health 的 `lifecycle_manager=launchd-pinned` 辨識管理權；標記存在即保留，即使 daemon 停止、標記損壞或 health 無法連線也不接管。App 不下載／安裝替代 CLI、不改原憑證、不因版本差異、信任刷新、登出、結束或更新而重新啟動它。daemon 也拒絕自行跟隨 App bundle、server 換版要求和 idle trust restart。

一般遷移與換版仍須完整 gate、排空、原身分及心跳驗證。沒有 gate capability 的首次導入依上方專用流程，不停止中央 Server。不能將 `active_task_count=0` 當作停止派工的證據。macOS 實機檢查未完成前，不得將 Linux fixture 的通過當作正式遷移成功。

## 隔離演練

使用 `scripts/deploy/test_update_daemon.py --old-binary OLD --new-binary NEW --work-dir NEW_LAB_DIR`。兩個 binary 由同一候選來源建置，使用測試版號 `0.8.1-zh-tw.901`／`.902`，皆須具有完整 build commit 與 gate。這些是測試版號，不是正式 release 或升級核准。演練以本機 HTTP fixture API、假 token、假 Claude CLI、隔離 HOME/profile/workspaces，以及唯一名稱的 user systemd lab unit 執行；不連正式 Server 或資料庫，不執行真 agent。

測試涵蓋原生 binary 與 hardlink launcher：在途 claim 延遲期間 gate 不 ready、完成交接後三次心跳仍無新 claim、解除後領取恢復；錯 hash 拒絕、既有 gate 不接管；成功換版保持 ID/runtime；候選通過版本查詢但啟動失敗時，還原原入口並重驗相同 ID/runtime、執行中 hash、固定 PID 與三次新心跳。`test_maintenance_release.py` 額外覆蓋解除後 fsync 失敗時必須回報已開放工作的回歸案例。

每次演練保存 JSON 與心跳時間序列，並清理唯一 lab unit 的 runtime link。HTTP fallback 有實際演練，WS／HTTP／legacy 共用的 claim 入口由鎖與既有單元測試核對；正式 Server、真資料庫、無 gate 舊版首次導入、macOS 實機及正式任務派工不在這個 fixture 演練內，部署前另行驗證。
