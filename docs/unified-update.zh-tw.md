# 繁中版配套更新維護

本文件記錄 HYC-26 的程式契約與重建方式。
只適用 `hyc5566/multica`。
公開 HTML 是一般說明正本。
`agent-update-guide.md.in` 是公開操作指引來源。
不再發布重複內容的 `download-update-guide.md`。

## 管理權

先辨識 App、執行中程序、CLI 入口及 daemon 的實際映像。
有 App 時，由 App 更新 App、內附 CLI 及 daemon。
無 App 時，由跨機操作端使用固定版本腳本。
Linux 沿用已核對的原 user 或 system unit。
不得新增游離 daemon 來完成更新。

既有獨立管理標記不會自動移除。
混合安裝及未知安裝必須停止。
同機不同帳號或 profile 不能作為跨機更新。
舊版缺少維護能力時，使用明確的首次導入入口。
中央 Server 持續運作，只更新已核對的目標機。
舊版來源可由核准盤點與實跑 hash 綁定；不虛構缺少的 commit。
備份並可還原原 wrapper／launcher 形態。
維護期間漏接或中斷由使用者重新驅動，不承諾自動補跑。
工具不得替操作者停止全 Server。

## 可驗證的證據

daemon health 提供 `update_attestation`。
它包含啟動映像 SHA-256、程序起始時間、成功心跳計數及有限的成功任務回傳紀錄。
`runtime_heartbeat_acks` 分開記錄每個 runtime。
三個 runtime 在同一輪各收到一次 ACK，不算三個週期。
驗證端須在固定 PID 及程序起始時間下，對所有原 runtime 觀察三次新前進。

待驗收或清理階段允許重新認證已重啟的 daemon。
先核對原生程序映像、UID、起始身分、完整配套、原 profile、Server、daemon、workspace 及 runtime。
閘門必須符合本交易已釋放的紀錄與目前開啟狀態。
先持久保存新的認證起點，再累積每個 runtime 三次新心跳。
成功派工必須來自同一個新程序，且在新的認證起點之後完成及回傳。
再次重啟時重新計數；不能沿用前一個程序的回執。
更新與回滾的待驗收及清理都適用。
無法核對可信配套時，保留復原資料並停止，不直接取消 PID 檢查。

可信原配套摘要在建立閘門前保存，重試及交換前重新比對，不覆寫已存值。
因此在交換前失敗並回滾，仍有可驗的原配套。
回復的舊 CLI 若不支援新交易 schema，使用本次候選的已驗證 CLI 接續。
先將該 CLI 放在 bundle 外的受管控制目錄；不能只依賴會在回滾清理時移除的候選。
使用原 journal/token 執行 `daemon coordinated-update`，保留副本直到終態已持久保存。
這是明確的人工接續路徑，不宣稱舊 CLI 能自動讀取新欄位，也不偽裝成 legacy 交易。

成功任務紀錄只來自本程序實際領取的工作。
它必須已成功回報 Server，且 Server 狀態確認為 completed。
terminal outbox replay 不能冒充新版派工證據。
沒有派工證據時，結果維持待驗收。

精確維護停止使用 `/shutdown/maintenance`。
請求綁定 PID、程序起始時間、daemon ID、profile、gate inode 與 owner token。
daemon 在 claim 鎖內核對 gate、ready、零 active 及零 in-flight。
任何欄位不符時拒絕停止。
一般 `/shutdown` 不能代替此契約。

## Linux 交易

開發入口是 `scripts/deploy/multica_update.py`。
它重用 `update-daemon.py` 的排空、換版及恢復機制。
發行時把兩個模組內嵌成一個 `multica-update.sh`。
使用者不需要另抓浮動的 Python 來源。

預設入口只產生計畫。
`--apply` 必須同時提供乾跑的 fingerprint。
apply 在鎖內重新盤點。
計畫不一致時，不開始換版。
`--accept` 即時核對同一筆交易及成功派工。
`--rollback` 只能選 ledger 的 immediate previous。

private inventory 提供服務、profile、搜尋邊界與資產位置。
這些值只是定位提示。
工具仍要核對實際程序、映像、UID、unit、runtime 及 state inode。
工具不能信任 `has_app=false` 之類的自證布林。
指定範圍無法檢查時，結果為 unknown 並停止。

缺省的 `inventory.inventory_scope=host` 保持完整全機盤點：執行中程序的 App 分流涵蓋所有 UID，不只目標帳號。
另一 UID 的 App 即使使用不同 inode、位於搜尋根目錄外，也會阻止獨立更新。
計畫、停止前及替換前都重新檢查。
原本同 inode 共用檢查保持獨立；另一 UID 的獨立 daemon 不會只因多 profile 而被當成共用目標。

明確設定 `inventory.inventory_scope=account` 才選用本人帳號模式。此模式僅支援 Linux binary layout，執行者的 EUID 必須等於原 owner UID，並沿用精確的原 user unit。共用 CLI、其他帳號的 App 及 system unit 不在更新目標。

在忽略已確認屬於其他 UID 且無法讀取的 `/proc` 映像之前，必須先正面證明：受管 root 為 owner 私有且其他人無法存取（owner-only 權限與安全祖先）、state 與受管檔案均屬本人，CLI、daemon 及 unit 同源；所有寫入、備份、回滾與清理均限本人範圍；owner 沒有 App、混裝、共享或未知目標。單憑檔案 owner 或 `no_shared` 自證不足。hardlink 或未知資產一律拒絕，不自動改權限或提權。無法確認 UID 或隔離時停止。

account 必須有 `systemctl` 與支援 typed JSON user-bus 查詢的 `busctl`，並核對原 owner 的 `/run/user/UID/bus` socket。從 user bus 查詢實際生效的 unit：`ExecCondition`、`ExecStartPre`、`ExecStartPost`、`ExecStop`、`ExecStopPost`、`ExecReload` 與 `DropInPaths` 均須為空，啟動 argv 與 profile 必須精確符合經核對的 canonical 值。不得把 systemctl 249 省略的空 Exec 陣列誤判為不支援，也不得忽略其他啟停 helper 或 drop-in。

account 計畫先證明私有 root、state、安全祖先及範圍，綁定 fingerprint；計畫階段不宣稱全域映像排他。state 或 unit 的 `FragmentPath` 是符號連結時拒絕；保留經核對的實體私有 root，不自動搬移 state、改權限或提權。可讀取的其他 UID 程序若使用同 inode，應提早拒絕。對同 UID 程序須可信核對 `/proc` status 的四個 UID 與起始時間；不能因 nondumpable 程序的 `/proc` 目錄顯示 root owner 就略過。App、混裝與未知目標仍拒絕。

停止原 unit 後，每次換檔或回滾換檔前，對 owner 檔案取得 Linux `F_SETLEASE` / `F_WRLCK` 可撤銷寫入租約，作為核心排他證據。活躍的 exec、open 或 mmap 會使租約以 `EAGAIN` 失敗；檔案系統不支援或失去租約時拒絕，不能退回較弱檢查。清理舊受管資產也須通過相同 guard 與租約。參見 [Linux F_SETLEASE 說明](https://man7.org/linux/man-pages/man2/F_SETLEASE.2const.html)。只有私有證明與這些檢查全部成立，才可不因已確認 foreign UID 的 `/proc` 映像 `EACCES` 單獨拒絕；不能據此宣稱不存在其他映像。

換檔以新 inode 複製，重驗 hash 並 fsync，再原子 rename，不覆寫舊 inode 的內容或權限。租約可能被撤銷；若已部分換檔，安全回滾並標示失敗，不能宣稱完整成功。fingerprint、停止前及替換前重驗、gate、排空、心跳、receipt、回滾及清理驗收均不放寬。

## 兩版保留

ledger 記錄本次實際交易的 current 及 previous。
第一次建立紀錄沒有虛構 previous。
同版重跑不輪替 previous。
更新失敗或尚待驗收時，保留復原所需資產。

全部驗收成功後，才清理更舊的受管程式檔。
清理前重新核對 ledger 歸屬、路徑邊界及 hash。
不要清理陌生檔案、profile、state、workspace、設定或憑證。
清理失敗必須保留可查結果，且回傳非零 exit code。

`data_compatibility=multica-profile-v1` 是本次未改 profile/config/state 序列化格式的契約。
新版 CLI、可信 manifest 及每個交易版本都要記錄它。
舊版首次納管需要核准的 baseline manifest 與獨立 SHA-256。
相容 ID 不同或缺少可信前版證據時，不允許自動回滾。
日後變更持久格式時，先設計遷移及恢復，再更新相容 ID。

## 頻道相容

新頻道仍使用 `/downloads/latest.json`。
它帶 `source_id=hyc5566/multica` 與 `coordinated_update_protocol=1`。
新版 App 資產放在 `coordinated_desktop`。
舊的 `desktop` 保持空物件。

這個分流是必要的首次導入保護。
舊 App 的 swap helper 會先刪自己的 rollback，且不協調實跑 daemon。
不能把新資產放回舊欄位，讓舊 helper 自行更新。
舊 App 須先使用明確的整組首次導入流程，建立可信 baseline 與 ledger。
新版 App 才可從配套頻道進行後續更新。

繁中版 `multica update` 不再直接執行舊 installer。
繁中 daemon 不再透過閒置 installer 自行換版。
`MULTICA_DAEMON_AUTO_UPDATE=false` 不是 gate，也不控制 App updater。
非繁中版的上游更新入口保持原契約。

## 發行與發布

1. 從核對過的單一 commit 建置全部程式。
2. 以固定正式版號保存 Linux amd64、Linux arm64、Darwin arm64 與 App。
3. 執行 `scripts/lan-release/package-unified-update.py`。
4. 執行 `scripts/lan-release/manifest.sh`。
5. 執行 `scripts/lan-release/finalize-unified-release.py`，再執行 `stage-latest.mjs`。
6. 核對產生的 HTML、操作指引、腳本、manifest 及 SHA-256。
7. 取得首相對候選及適用實機證據的放行。
8. 同次發布文件、腳本、checksum 及最後的 latest 頻道。

`publish-latest.sh` 預設只預覽。
`build.sh` 先產生帶固定版號的 raw binary 與基礎建置資料。
Mac 原生打包後，以 App 內附的已驗證 CLI 作為同版 Darwin standalone 成品。
必須在全部資產齊備後才產生最終 SHA256SUMS。
不要發布舊的直接換檔 installer。
正式 `--apply` 前，必須已有不可變版本資產及回滾備份。
公開 `.md` 的精確路由須回傳 `text/markdown; charset=utf-8`。
發布後以可信 CA 驗證頁面、指引、腳本及 checksum 為 HTTPS 200。
下載腳本的實際 hash 必須與版本清單相同。
至少一台已導入配套協定的 App 要收到核准候選。

本文件、測試與候選不代表已部署。
HYC-39 的 `.3` 成品保留為歷史建置結果。
它不代表通過 HYC-26 的新增要求。
