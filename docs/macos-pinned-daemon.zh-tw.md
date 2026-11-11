# macOS：獨立釘選 daemon

> 此方案已由 HYC-26 的 App 配套更新政策取代。不要依本文件新增或遷移獨立 daemon。既有標記與安裝應先盤點，不自動刪除。最新流程見 [配套更新維護](unified-update.zh-tw.md)。以下保留歷史技術資料。

本程序適用於支援 `launchd-pinned-v1` 的 Multica 繁中版。選定的 A 案以使用者 LaunchAgent 執行固定版本的 daemon；App 僅連到原 profile，App 更新不改 daemon 版本。這份文件是候選操作指引，不表示已發布或已遷移任何 Mac。安裝、換版和服務中斷仍須另取得擁有者核准。

## 安裝形態與操作前提

- 固定資產：`VERSIONS/<FULL_COMMIT>-<SHA256>/multica`，每版獨立目錄；不覆蓋既有版本，不以 App bundle 作為長期 daemon 執行檔。保留舊版供回滾。
- 管理者：`~/Library/LaunchAgents/LABEL.plist`，在 `gui/UID` domain 以前景模式執行同一 profile。LaunchAgent 在使用者登入後工作；不保證登出或登入前仍執行，不安裝 system daemon。
- 原身分：沿用 `~/.multica/profiles/PROFILE` 的原設定、daemon ID、登入憑證及 workspace/runtime 註冊，不重新登入、建立 profile 或複製別人的身分。既有 state symlink 保留，切換時固定實體目錄 inode；非預設 config root 不在工具支援範圍。
- 管理標記：原 profile 的 `daemon-management.json` 宣告獨立管理。App 對任何存在的標記一律保留管理權，包括格式錯誤、不可讀或 daemon 停止。不得為了讓 App 啟動 daemon 而刪掉標記。
- 環境：LaunchAgent 明確給定原帳號 HOME、經核准的 CA 檔案、agent CLI PATH、工作目錄及日誌路徑。CA 必須在 App bundle 外持續可用；核對原 CA 的指紋及信任效果，不在文件、核准包或日誌中放憑證內容。

先取得固定的來源 commit、binary SHA-256、回滾資產、profile、服務 label、原 daemon ID／runtime 集合，以及可中斷窗口。這些實際值放在私人核准包；以下全部使用大寫佔位值。操作者必須是 Mac 的擁有者，從自己的互動終端執行，不得從被更新 daemon 或同機其他 profile 的任務執行，也不得移除 task 環境標記繞過限制。

一般遷移保持核准的原 CA **路徑與 SHA-256**、原 PATH，不會自動搬移 CA。原 CA 若位於會被 App 更新替換的 bundle，須先另外核准並完成移到持久位置的信任維護，重新取得基準，再執行本程序。不能直接以另一個 CA 或未查證的 shell PATH 代替原 daemon 環境。

遷移前須先安裝支援本契約的 App 並完整結束 App。舊 App 不識別管理標記，禁止與獨立 daemon 並用；僅更新 daemon 無法約束舊 App 的生命週期。不要在遷移中途開 App、改登入帳號、切 Server、更新 CA 或同時執行另一個維護工具。

## 離線準備固定版本

使用 Python 3 標準函式庫；沒有線上下載或套件升降版。先準備並核對 binary、目錄、CA 與舊版資產，再執行：

```sh
python3 scripts/deploy/stage-macos-daemon.py \
  --binary "$APPROVED_BINARY" --destination "$VERSIONS" \
  --sha256 "$BINARY_SHA256" --version "$TARGET_VERSION" --commit "$FULL_COMMIT" \
  --label "$LABEL" --profile "$PROFILE" --ca-cert "$CA_CERT" \
  --path-env "$APPROVED_PATH" --working-dir "$WORKING_DIR" --log-dir "$LOG_DIR"
```

工具核對 SHA-256、`version --output json` 的完整 commit 與 gate／lifecycle capability，產出固定 binary、`launch-agent.plist` 與 `daemon-management.json` 範本。它不改原 profile、不載入服務、不停止 daemon。設定 `STAGED_DIR` 為輸出目錄，先檢閱 plist 的確切 binary、profile、HOME、CA、PATH、日誌及目錄。不要將模板直接覆蓋同名的其他服務。

## 有維護閘門時的一次性遷移

執行中的舊 daemon 必須實測回報 gate capability、profile、完整 commit 與原身分。先完整結束 App，保留原 App bundle 的確切 CLI 路徑及雜湊；觀察 CLI 必須是獨立、固定且不被此次替換的 binary。

```sh
python3 scripts/deploy/manage-macos-daemon.py migrate \
  --profile "$PROFILE" --profile-dir "$PROFILE_DIR" --label "$LABEL" \
  --plist-path "$PLIST_PATH" --candidate-dir "$STAGED_DIR" \
  --cli "$OBSERVER_CLI" --health-url "http://127.0.0.1:$HEALTH_PORT/health" \
  --evidence-dir "$NEW_EVIDENCE_DIR" --original-ca-cert "$CA_CERT" \
  --original-ca-sha256 "$ORIGINAL_CA_SHA256" --original-path-env "$APPROVED_PATH" \
  --app-executable "$APP_EXECUTABLE" --app-quit-confirmed
```

工具必須在停止前取得自己的 gate，等到 blocked、ready、零 in-flight／active，同時核對原 PID、binary、daemon ID、profile、完整 runtime 集合。標記先保留管理權，再只向已核對的舊 PID 發送正常終止訊號，確認舊程序消失才啟用確切 LaunchAgent。不得使用 executable 名稱終止程序，不得強殺任務，不得同時執行兩個 daemon。

候選啟動後仍保留 gate。核對 launchd PID／實際 binary、SHA、版本、完整 commit、原 daemon ID、原 runtime 集合、管理者及 protocol；以新程序註冊後的時間為基準，至少觀察三次所有 runtime 的 heartbeat 前進，確認 PID 穩定。通過後才解除自己持有的 gate。失敗時只對已證明受本工具管理的候選進行復原；不能證明身分時停止並保留 gate、標記與證據，交擁有者處理。

`NEW_EVIDENCE_DIR` 必須尚不存在且父目錄已存在。工具保留原標記／plist（原先有獨立服務時）、原與候選 binary 身分、runtime 基準、心跳序列及結果；不複製登入設定或憑證。實際執行檔同時比對 ps 的 UID／啟動時間、lsof 映射的 device／inode、檔案雜湊與版本，不只比對路徑。每次重啟重新讀取 profile 的 `daemon.port`，不假定 health port 不變。

## 缺少維護閘門時的首次導入

缺少 gate 時，一般遷移工具會拒絕。
`active_task_count=0` 或 HTTP 200 不能證明停止領取。
新導入採 [App 統管配套](unified-update.zh-tw.md)。不要再安裝獨立 daemon。

由跨實體機操作者使用經核准的 App 整組首次導入流程。
先核對實跑映像與原身分，盡量等待已知工作，再備份完整舊 App 及原 CLI 入口。
中央 Server 持續運作。只停止已核對的目標 App 及 daemon。
候選啟動前建立新版 gate，驗完整配套、原身分及三個心跳週期後才恢復接單。
再驗一次已授權的派工及回傳。缺證據時維持待驗收。

失敗時恢復完整前版及原入口。
若前版缺 gate，須記錄它可能立即領取工作。
維護交界的漏接或中斷由使用者重新驅動，不承諾自動補跑。
既有獨立管理標記及 LaunchAgent 的轉換仍需專屬盤點，不由一般使用者自行刪除。

## 升級與回滾

先離線 stage 新版，再對已由 launchd 釘選的 daemon 使用同一組參數執行 `update`。`rollback` 的候選目錄選擇保留的舊固定版本；它同樣要求目前 daemon 與回退版都支援 gate，並經過排空、原身分和三次心跳驗證，不是無條件切換 symlink。

```sh
python3 scripts/deploy/manage-macos-daemon.py update \
  --profile "$PROFILE" --profile-dir "$PROFILE_DIR" --label "$LABEL" \
  --plist-path "$PLIST_PATH" --candidate-dir "$STAGED_DIR" \
  --cli "$OBSERVER_CLI" --health-url "http://127.0.0.1:$HEALTH_PORT/health" \
  --evidence-dir "$NEW_UPDATE_EVIDENCE_DIR" --original-ca-cert "$CA_CERT" \
  --original-ca-sha256 "$ORIGINAL_CA_SHA256" --original-path-env "$APPROVED_PATH"

python3 scripts/deploy/manage-macos-daemon.py rollback \
  --profile "$PROFILE" --profile-dir "$PROFILE_DIR" --label "$LABEL" \
  --plist-path "$PLIST_PATH" --candidate-dir "$PREVIOUS_STAGED_DIR" \
  --cli "$OBSERVER_CLI" --health-url "http://127.0.0.1:$HEALTH_PORT/health" \
  --evidence-dir "$NEW_ROLLBACK_EVIDENCE_DIR" --original-ca-cert "$CA_CERT" \
  --original-ca-sha256 "$ORIGINAL_CA_SHA256" --original-path-env "$APPROVED_PATH"
```

換版只卸載精確 label，先確認舊 PID 不在，再載入新 plist；保留上一版的 plist、標記及 binary。自動恢復也必須驗證原入口和原身分。若 gate 解除後才發生錯誤，工作可能已恢復，必須重新取得屏障並排空，不能逕自再次停止。任何失敗都停止後續批次。

## App、daemon 與 Server 的相容範圍

| 組合 | 行為與允許條件 |
| --- | --- |
| 支援本契約的 App + `desktop_protocol=1` 的釘選 daemon | 允許連線；binary 版本不同不自動換版。App 更新、結束、登入／登出均不接管 daemon。 |
| 新 App + 釘選 daemon 缺少／未知 protocol | 顯示可見的不相容提示，仍不替換或重啟；由擁有者核對契約並更新合適的一端。 |
| daemon 停止、health 暫時失聯或標記損壞 | App 顯示獨立管理狀態，不下載或啟動另一個 daemon；由擁有者診斷。 |
| 舊 App + 釘選 daemon | 不允許。舊 App 不識別管理權；遷移前必須換為支援版並結束 App。 |
| daemon + 不同版本的 Server | 不憑 semver 保證相容。保留既有 API／註冊和 heartbeat 協定，核准包必須逐組驗證登入、workspace/runtime、三次心跳、派工和回傳；失敗不得解除 gate 或放行下一批。 |

`desktop_protocol=1` 僅表示本機 health 與 App 連線契約，不是 Server capability 或任意未來版本的認證。fixture 測試證明本機生命週期和維護流程，沒有驗證任意歷史 Server。App 的帳號切換不會改 daemon 憑證；帳號或 CA 需要變更時由擁有者另外安排維護。

## Mac 實機驗收清單

下列項目必須由擁有者在核准窗口完成；Linux 的單元測試、plist 解析和交叉編譯不能代替。

- [ ] 確認 Apple Silicon 成品來源、完整 commit、SHA、簽章／Gatekeeper、隔離屬性與執行權限；獨立觀察 CLI 可用。
- [ ] 確認 launchctl domain／label、lsof 對執行檔的辨識、ps 的 UID／PID 與健康資訊一致；同 profile 唯一程序。
- [ ] 有 gate 時驗證停止領取與自然排空；無 gate 時採明確的舊版專用首次導入流程，中央 Server 持續運作。確認原 profile、daemon ID、CA、全部 runtime 未改。
- [ ] 成功遷移、固定 PID、至少三次實際 heartbeat 前進、解除 gate 後一次授權派工及結果回傳。
- [ ] App binary 較新與較舊但 protocol=1 均可連線；未知 protocol 有可見提示且無重啟；對實際核准 Server 版本驗證登入及工作回傳。
- [ ] App 手動重啟、自動更新／復原、版本差異、閒置 reload、信任刷新、登入／登出與結束，都不變更獨立 daemon 的 PID／binary／標記。
- [ ] daemon 停止、啟動中、health timeout、標記損壞時，App 不下載、替換、重啟或建立第二個 daemon。
- [ ] OS 重啟／重新登入後由 launchd 回到同一個固定 binary、profile、ID 與 runtime；確認登出期間的可用性符合使用者需求。
- [ ] 人工製造候選無法啟動、hash／身分不符、heartbeat 不前進，驗證拒絕或回復原版；復原仍有 gate、原身分及三次新心跳證據。
- [ ] 已釘選版升級及回滾至保留版本各一次；回到 App 管理的獨立撤回程序確認只有一個 daemon，沒有殘留 launchd job。
- [ ] 保留日誌、核准包與檢查結果；不收集 token、CA 內容或 agent 登入資料。不以成功啟動取代完整驗收。

LaunchAgent 的背景行為依 [Apple 啟動工作說明](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html)；實際 OS 版本仍須現場核對。
