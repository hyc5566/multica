# 自架 Multica CLI／daemon 安裝與更新指南

本站：`https://10.1.24.90:45671`。固定指南網址：
`https://10.1.24.90:45671/downloads/selfhost/guideline.md`。
本指南是操作說明；實際換版須有目標機、版本、停機窗口及回復方案的核准。

## 新機安裝

目前獨立 CLI 成品為 **Linux x64**，版本 `0.9.0-zh-tw.1+up.013be77`，來源提交
`54c0d024bf49f0e107be6d974ae3c0969199cd17`，自家 GitHub Release
`hyc5566/multica` 的同版 CLI archive；不使用上游安裝程式。App 目前僅提供 macOS Apple Silicon。
有 App 的機器由 App 一併管理 App、CLI 及 daemon，不另裝獨立版本。

先安裝 Git 2.40 以上及 OS 的 ca-certificates。若 HTTPS 憑證尚未受信任，向管理員取得並核對
**公開** s90 CA，SHA-256 應為
`e1ffc707220f8dfa00ffa4b7c9699d9b93fd1d407d065bf8801c3cea67037e6d`。
可將公開 CA 加入系統信任庫，或在下載時使用 `curl --cacert /已核對的公開CA檔`，
並在執行腳本時設定 `MULTICA_INSTALL_CA_FILE=/已核對的公開CA檔`。
不得用 `--insecure` 跳過可執行檔的 TLS 驗證。

```sh
curl -fsSL https://10.1.24.90:45671/downloads/selfhost/install.sh -o install.sh
bash install.sh
```

腳本核對 archive SHA-256
`260da62ecc572d994d6a793647a709623eefab2559d2e65242d90da0872c0d1e`，
安裝至 `~/.local`；可設定 `MULTICA_INSTALL_ROOT` 為另一個絕對路徑。
已存在的安裝一律拒絕覆蓋。腳本不登入、不啟動 daemon、不修改既有設定。
產生的 `bin/multica` 入口固定 Server／App URL 為本站，帶入已核對的公開 CA，
停用自動換版及自動重載。將該 `bin` 加入 PATH 後：

```sh
multica version
multica login
multica auth status 2>&1 | sed -n '/^Server:/p'
```

`auth status` 的原始輸出可能含 token 前綴，因此上例只輸出 Server 行，不保存完整輸出。
只記錄去敏後的 Server URL 與版本。`login` 需本人完成瀏覽器登入；無瀏覽器時依
`multica login --help` 使用 `--token`，token 由既有安全管道傳入，不放進報告、歷史或日誌。
完成註冊、確認原 daemon 身分及核准啟動後，才依 `multica daemon --help` 與既有服務方式啟動。

## 更新沒有 App 的機器

1. **跨機操作**：由另一台實體機上的 agent 操作目標機。不得更新或重啟自己所在機台的 daemon；
   同機不同帳號、profile 仍算自機。s90 執行中的 agent 不得停止自己的 daemon；交其他機台或本人執行。
2. **唯讀盤點**：核對 hostname、帳號、App 是否存在、CLI 入口的實體 binary、版本、SHA-256、
   daemon_id／runtime_id、Server URL、工作目錄及服務管理方式。混合或未知安裝先停下。
   Linux 精確核對原 systemd unit 的名稱、user/system scope、MainPID 及 `/proc/<PID>/exe`，
   只記錄核對結果，不列出整份環境或設定。不得按 executable 名稱殺程序。
3. **固定成品**：以自家 `hyc5566/multica` 經核准 Release 或 s90 HTTPS 成品為準，
   固定完整版號、來源 commit、平台與 SHA-256；不得回退下載上游或混用 App／CLI 提交。
   本指南的新機 installer 不作既有 daemon 更新器，也不自行清除舊版。
4. **先保留舊版**：備份舊 binary、launcher、對應公開 CA、非秘密的版本／hash 清單及還原步驟。
   設定、註冊身分與憑證保留原位置；若需備份，僅由既有安全機制製作受保護副本，不讀出內容。
   確認備份可取得且 hash 相符，再把候選放到獨立版本目錄。
5. **停止領取與排空**：依 repo `docs/daemon-maintenance.zh-tw.md` 及該版本
   `scripts/lan-release/update-daemon.sh` 的參數契約建立可驗證維護屏障；
   先查 `--help`，提供原 daemon 身分、固定 commit、獨立觀測 CLI、health URL 與 evidence 目錄。
   確認沒有執行中任務，已停止領取新任務。不能證明屏障、缺 gate capability 或缺核准就停止，
   不用直接換檔繞過；舊版首次導入另依已核准程序。中央 Server 保持運作。
6. **切換與啟動**：在核准窗口依既有服務切換到已核對候選；沿用原精確 systemd unit，
   不新增第二個 daemon、不刪除原 profile、不重新註冊以掩蓋身分變動。
   這一步由遠端操作者執行，不能由正在被換版的 daemon 執行。
7. **驗證**：比對實際執行檔及 hash、完整版號、原 daemon_id／runtime_id 與本站 Server URL，
   檢查 health、RestartCount 及錯誤。透過獨立觀測端逐次讀回 **至少三個實際心跳週期**，
   記錄每次時間戳且持續前進；不是把同一個 last_seen 值讀三次。
   再執行 **一次已授權派工與結果回傳**，核對 run、目標 runtime 及結果。
   不自行建立新 agent、憑證或測試任務；需要時請首相提供已授權試派。
8. **失敗即回復**：任一驗證失敗，停止下一批，保存去敏證據。依核准方案停止該目標的服務，
   將 launcher／unit 指向已備份舊 binary，還原原設定連結，沿原精確服務啟動；
   再核對舊版 hash／身分、三次實際心跳及一次派工。只回復 binary 不代表 DB 或資料已恢復。
   保留失敗候選及日誌供排查；沒有可行回復方案就不開始更新。

所有步驟不得讀出或記錄 token、密碼、私鑰、`.env`、完整 auth/config JSON 或整份環境變數。
驗收全部通過後再依已核准保留政策處理舊版，不自動刪除備份。

## 禮部後續替換指南

本檔以 UTF-8 純文字供應，**可獨立替換，不重建 Web**。正式位置為既有 Web downloads 掛載目錄中的
`selfhost/guideline.md`；部署方案列出主機實體路徑。維護者先備份舊檔與 SHA-256，
在同一目錄寫入 `guideline.md.next`，核對內容後以 `mv` 原子替換（目錄 bind，不是單檔 bind）。
不改權限、憑證或 App `latest.json`。隨後從固定 HTTPS URL 下載讀回，比對 SHA-256 及必要條款。
回復時以相同方式換回備份內容。瀏覽器及代理應遵照 `Cache-Control: no-store`；
請用實際 HTTP 回應及內容 hash 確認更新，不以本機檔案存在取代驗證。
