# 繁體中文版 Server 平順切換維運程序

本文件是繁體中文版 Server 新舊版本交接的主要操作規範。後續 agent 或維護者進行 Server 更新前，先閱讀本文件，逐項核對現場，再執行切換。操作工具為 [server-handoff.py](../scripts/deploy/server-handoff.py)，可重現演練為 [test-server-handoff.py](../scripts/deploy/test-server-handoff.py)。

「平順切換」是先讓新版就緒，再切換入口，保留舊版處理既有連線並提供回復能力。它不會把既有 WebSocket 搬到另一個程序，也不代表任意版本、資料庫變更或故障情況都能零中斷。

## 1. 適用範圍與目前進度

適用於單機 Docker、共用 PostgreSQL／Redis／上傳檔案、Caddy 代理，且新舊映像的 migration 檔案內容完全相同的 Server 更新。Web／Desktop 發布另行安排，不因 Server 切流成功就宣稱它們已更新。

2026-09-10 的驗證基準：

- Server／Web `.41.4`（`b8f2144f9`）已部署至 s90；這與平順切換基礎設施初始化是不同操作。
- 工具提交 `2a56531f4` 已完成隔離演練：兩個相同 `.41.4` Server、獨立 PostgreSQL、具密碼的 Redis、Caddy。
- 160 次公開／本機入口 HTTP 探測零錯誤；原已登入 WebSocket 經切換與回復仍可收送，新連線可登入；新版建立的 issue 事件送達舊節點連線。
- 3 項真實 Redis 跨節點請求、usage 結果讀回與原子領取測試通過，沒有跳過；錯誤 Caddyfile 掛載來源會被拒絕。
- **截至本文件建立時，s90 正式環境尚未啟用 Redis／雙入口代理初始化。** 下次操作需重新查核，不能把隔離演練或文件中的命令當成已上線證據。

以上只證明同一版本的交接機制與短時間觀測；未驗證任意混版相容、正式負載、完整五分鐘連線排空，或長時間 terminal／SSE 工作階段。

## 2. 每次執行前的必要條件

1. **現場與來源：**確認 hostname、帳號、repository remote、branch、完整 commit、乾淨或明確標示的 working tree，以及實際執行中的映像 ID。開發與成品準備使用隔離 worktree，不直接修改正式來源。
2. **授權：**核對本次目標、版本、服務影響及失敗回復已在授權範圍內。相同範圍已有明確核准時直接執行，不逐條重問；新增共用服務、入口設定、資料遷移或更大中斷範圍時，先備妥具體方案再確認。文件本身不授予部署或推送權限。
3. **混版相容：**除了 migration 全檔雜湊相同，還須審查 API、背景工作、事件格式、請求儲存格式及工作階段相容性。工具不能代替這項審查。
4. **共享狀態：**所有 Server 使用相同具驗證保護的 `REDIS_URL`，並採 `REALTIME_RELAY_MODE=sharded`（預設）或已確認相容的 `dual`。request stores、liveness 與跨節點事件不能各留在記憶體。channel lease 維持相同的 PostgreSQL 後端，不混用不同 lease 後端。
5. **入口：**公開 API、daemon 本機 API 與 Web 內部 API 都經過同一切換入口，不得有呼叫端直接連到待退役的 Server。
6. **回復：**舊映像、設定及必要資料備份實際可取得；寫明觸發條件、步驟、驗證與容許的中斷。不能只記「重新部署舊版」。
7. **操作互斥：**確認沒有其他人同時部署。工具以 Caddyfile 旁的 lock 防止相同設定同時切換；不繞過鎖或平行更新共用 Git／服務狀態。

任何必要條件不成立，就保留舊版服務並處理缺口。不得用略過檢查、停用 TLS 驗證或未受保護的歷史改寫完成部署。

## 3. 首次初始化：先建立共享狀態與穩定入口

這是一項獨立於一般版本更新的一次性正式設定變更；初次導入仍可能短暫重連。完成並驗證後，才使用第 4 節的一般切換程序。

### s90 的入口對照

| 入口 | 初始化後的目標 | 注意事項 |
| --- | --- | --- |
| `https://10.1.24.90:45671` | Caddy → 目前 Server | 保留既有 TLS／CA 與網址 |
| `http://127.0.0.1:45673` | Caddy 本機 HTTP listener → 目前 Server | 必須把原 backend 的 host port 移交給 Caddy；不得暴露到所有網卡 |
| Web `REMOTE_API_URL` | `http://caddy:45673` | 包含 SSR 與本機 Web 的 API 路徑，不能仍指向舊 backend |

Redis 僅使用受限 Docker 內網，不發布 host port；設定驗證密碼、持久化 volume、AOF、`noeviction`、健康檢查與合理記憶體上限（s90 候選為 256 MiB）。映像釘選已審查的 digest。密碼在核准執行時產生，存入受保護設定，不能寫入 Git、文件、命令輸出或 issue。Redis 是新增共用依賴，必須監控可用性及容量；單機 Redis 不提供跨機故障容錯。

兩個 API 代理區塊都須預先載入 `stream_close_delay 5m`，例如：

```caddyfile
reverse_proxy @backend backend:8080 {
    stream_close_delay 5m
}
```

本機 listener 也使用相同 delay 與 upstream。Caddy 預設在設定重載時關閉 WebSocket；此選項延後關閉，不能無限保留連線。第一次加上 delay 時，原本未設定 delay 的連線仍可能中斷，見 [Caddy 官方串流說明](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#streaming)。Caddy admin API 保持在容器內，不公開 port 2019。

初始化依序執行：

1. 備份目前 `.env`、Compose override、Caddyfile 與映像識別；含秘密的備份限制存取權限。
2. 確認現有模型清單、usage、update、skill 等請求已完成；原記憶體中的 pending requests 不會自動搬進 Redis。若無法排空，另定重試／重連窗口。
3. 啟動 Redis，驗證使用 Server 相同憑證的 PING，再準備 Server `REDIS_URL` 與全部入口設定。
4. 受控重建 backend，啟用 Redis 並釋放原本機 host port；確認 Server 就緒後重建 Caddy，讓它接管公開與本機入口，再更新 Web 內部 API 目標。s90 原有 Caddyfile 為單檔 bind mount，寫入需保留 inode。
5. 按第 5 節驗證共享狀態、兩入口、登入連線、daemon 身分與持續心跳；更新正式維運紀錄後，才標示初始化完成。

初始化失敗的回復順序：還原原設定，先重建 Caddy 釋放本機 API port，再重建原 backend 與 frontend，重新驗證。保留 Redis volume 供診斷，不附帶刪除資料。此回復與一般切流回復不同，必須在當次部署方案寫明。

## 4. 一般更新：預覽、啟動候選、切流

從包含工具的 repository 根目錄執行。s90 使用指定 Python：`$HOME/miniconda3/envs/hungyu/bin/python`。先核對它可執行，不改用未知的系統環境。

準備並記錄以下參數，範例中的大寫值都要以現場查核結果取代：

| 參數 | 來源 |
| --- | --- |
| `CURRENT_CONTAINER` | 目前實際服務的 Server 容器名稱 |
| `CURRENT_PROXY_ALIAS` | Caddy 目前載入的 upstream DNS 名稱；可能不同於容器名稱 |
| `UNIQUE_NEW_CONTAINER` | 本次專用且未被使用的候選名稱 |
| `sha256:REVIEWED_64_HEX_IMAGE_ID` | 已建置、測試、審查的本機不可變映像 ID |
| `CADDY_CONTAINER`、`REDIS_CONTAINER` | 已核對的正式容器名稱 |
| `/absolute/path/Caddyfile` | 實際 bind mount 至 `/etc/caddy/Caddyfile` 的 host 檔案 |

先執行預覽：

```sh
"$HOME/miniconda3/envs/hungyu/bin/python" scripts/deploy/server-handoff.py \
  --active CURRENT_CONTAINER --active-upstream CURRENT_PROXY_ALIAS \
  --candidate UNIQUE_NEW_CONTAINER --image sha256:REVIEWED_64_HEX_IMAGE_ID \
  --caddy CADDY_CONTAINER --config /absolute/path/Caddyfile \
  --redis REDIS_CONTAINER --delay-seconds 300
```

預覽會檢查執行狀態、Redis 驗證 PING、目前 Server `/readyz`、migration 內容、實際 Caddyfile 掛載來源及已載入的代理／delay。它會啟動只讀 migration 清單的一次性映像程序，不附帶正式網路憑證或資料掛載，不切換流量。

預覽通過，且混版審查與部署授權仍有效後，執行相同命令並加上：

```text
--confirm-compatible --apply
```

工具會：

1. 以權限受限的暫存檔繼承環境，使用同一網路與上傳檔案掛載；不把秘密印出或放進命令參數。多行環境值與不支援的設定形式會拒絕處理。
2. 使用不可變映像執行 `/app/server`，略過自動 migrator；新舊 schema 必須已一致。
3. 等候候選 `/readyz` 通過並核對映像，再保存 `Caddyfile.before-CANDIDATE`。
4. 同時改寫公開與本機 API upstream，驗證 Caddy 設定後 reload，保留舊 Server。
5. 設定驗證／reload 失敗時，恢復並重新載入原設定。候選啟動失敗則不切流，保留候選供診斷。

工具輸出的 `switched: true` 只代表切流命令完成，不代表第 5 節驗收已完成。保存輸出中的映像、回復檔位置與舊容器名稱。工具不會自動終止舊 Server。

## 5. 切流後驗證

| 項目 | 必須取得的證據 |
| --- | --- |
| 兩個入口 | 公開與本機 `/health` 的 commit、started_at 對應候選程序；不能只看 repository HEAD |
| 就緒 | 對實際 Server `/readyz` 取得 DB／migration 就緒結果；若公開路由未提供此端點，從容器內驗證，不把 Web 的回應當成 readyz |
| 程序與日誌 | 實際 image ID、啟動時間、RestartCount，以及沒有持續崩潰或重啟循環的觀測證據 |
| 共享狀態 | Redis 可用；跨節點 pending request／usage 結果可讀回；readyz 本身不涵蓋 Redis |
| 即時連線 | 原已登入 WS 仍能收送、新連線可登入；新版事件能到達舊節點連線，不能只做握手 |
| Daemon | 身分與註冊仍正確，last_seen_at／heartbeat 在觀測期間持續前進，且實際派送／回傳成功 |
| 特殊工作階段 | 檢查 terminal、SSE、串流及長時間請求；無法確認可持續者需個別處理或已核准的重連窗口 |

觀測窗口依本次風險與核准方案決定，記錄開始／結束時間及通過條件。不以一次成功探測宣稱長期穩定；未測到的項目明列未執行。

## 6. 回復流量

新版未就緒、入口指向錯誤、共享事件遺失、daemon 心跳未恢復或持續錯誤時，停止後續擴散，依當次已核准方案回復。

確認工具記錄的備份存在後，將其內容**原地寫回**實際掛載的 Caddyfile，避免以 rename 更換 bind mount 的 inode。以下大寫值須填入實際記錄：

```sh
cat SAVED_CADDYFILE > /absolute/path/Caddyfile
docker exec CADDY_CONTAINER caddy validate --config /etc/caddy/Caddyfile
docker exec CADDY_CONTAINER caddy reload --config /etc/caddy/Caddyfile
```

逐條檢查結果；validate 失敗不可繼續 reload。重新驗證公開與本機入口都回到舊程序、WS 與 daemon 心跳恢復。保留新版程序至其既有連線也排空，不在回復入口後立刻終止它。

這是路由回復，不會還原資料。工具拒絕 migration 變更；若本次涉及其他資料變更，須另有資料恢復方案，不能宣稱切回 binary 就還原了資料。

## 7. 舊版退役與正式設定一致性

舊 Server 至少保留設定的 delay（預設五分鐘），再確認舊入口、連線、terminal／串流及進行中請求均已排空。不能只因五分鐘到了就終止程序。Go 一般 HTTP shutdown 與已升級的 WebSocket 處理不同；工具不會代替工作階段判斷。

完成判斷後，依已核准範圍明確退役指定舊容器，保留可回復映像與設定。候選由 `docker run` 建立，具 restart policy，但**不會自動成為 Compose service**；必須同步正式 Compose／發布清單與操作入口。未完成同步前，不盲目執行 `compose up backend`，以免重新建立舊版本或讓入口回到舊別名。此同步若需要再重建服務，納入當次切換計畫，不附帶執行。

## 8. 留痕、完成判定與日後重建

紀錄至少包括：時間與時區、hostname／帳號／agent、核准依據、舊新完整 commit 與映像 ID、工具版本、實際入口／容器名稱、測試及觀測窗口、備份位置、回復／退役結果、未驗證限制。秘密一律不入紀錄。

只有本次要求及適用驗證完成、必要文件保存成功後，才記為完成；候選完成、待初始化、待核准或回復成功但原升級失敗，應各自標示。s90 過程放入 `$HOME/docs/work-notes/`，完成後才歸檔至 `$HOME/docs/records/`；主操作正文維持在本 repository，機台筆記只連結及補充現況。

重建繁中版時保留本文件、[維護總覽](zh-tw-maintenance.md)、`CLAUDE.md` 的閱讀入口及 `scripts/deploy/` 工具／演練。更新工具參數或部署架構時，同一提交更新本文件；不要另維護一份互相漂移的步驟。

可重跑隔離演練（需 Docker、Go，以及預先取得的 PostgreSQL／Redis／Caddy／已審查 Server 映像）：

```sh
"$HOME/miniconda3/envs/hungyu/bin/python" scripts/deploy/test-server-handoff.py \
  sha256:REVIEWED_64_HEX_IMAGE_ID
```

它使用獨立網路、隨機 loopback port、虛構會員與測試憑證，完成後清理測試容器、匿名 volume 與網路，不讀取正式環境檔。僅修改文件時核對內容、連結及命令參數即可，不為文件變更啟動服務演練。

有 migration 差異的後續升級，另行設計向前／向後相容的 expand/contract 遷移與資料恢復；不可繞過本工具的相同 schema 門檻。

本次版本交接演練可指定新舊兩個不可變 image ID：

```sh
python scripts/deploy/test-server-handoff.py sha256:OLD_IMAGE sha256:NEW_IMAGE
```

省略第二個參數時使用同版演練；正式升級應傳入實際新舊版本。
