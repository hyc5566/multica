# 繁中版下載鏡像與獨立入門指南

> HYC-26 起，最新發布契約見 [配套更新維護](unified-update.zh-tw.md)。新頻道使用 `coordinated_desktop` 並把舊 `desktop` 留空，避免舊 App 執行不協調的 swap。先以 `package-unified-update.py` 產生單檔腳本及固定 manifest，再產生頻道、HTML 和操作指引。下方舊的 installer、獨立釘選及舊 App 自行重載說明屬歷史資料，不作為新部署程序。

公開操作正本為生成的 `agent-update-guide.md`。它與頁面、腳本、checksum 及 latest 頻道同次發布。Caddy 精確路由來源是 `scripts/lan-release/Caddyfile.update-snippet`；套用前核對現場，validate 通過才可 reload。必須保留現有 TLS、代理路由及 bind mount inode。`.md` 回應要帶 `text/markdown; charset=utf-8`。實際發布仍須適用實機驗證及首相放行。

主要內容來源是 [getting-started.zh-tw.html](getting-started.zh-tw.html)。這份 HTML 不需要 JavaScript 或外部字型，可單獨開啟。頁面目前說明 `0.4.43-zh-tw.7`；發布新版本前須更新版本、平台、安裝器、CA 與連結，再重新驗證。

## 歷史 s90 發布狀態（2026-09-30）

2026-09-30 在 `linker-SYS-540A-TR` 唯讀核對：正式 Caddy 已經供應下列入口，本次分支整合沒有修改或重載服務。

| 公開路徑 | Caddy 持久卷來源 |
| --- | --- |
| `/download` | `/config/hyclv159/download.html` |
| `/download/guide`、`/download/guide/` | `/config/hyclv159/guide.html` |
| `/download/install.sh` | `/config/hyclv159/releases/0.4.43-zh-tw.7/install.sh` |
| `/downloads/0.4.43-zh-tw.7/*` | `/config/hyclv159/releases/0.4.43-zh-tw.7/*` |

公開網址是 `https://10.1.24.90:45671`。正式 `Caddyfile` 位於 `/mnt/data-home/hungyu/multica-zh-tw/production/Caddyfile`；路由與資料都在 Caddy，不由一般 frontend build 自動供應。新 frontend 上線前，先決定是否保留 Caddy 的 `/download` 精確路由；該路由目前會優先於 Next.js 頁面。

目前正式 `guide.html` 與本檔來源的 SHA-256 均為 `018541ea0e6afde48b39edf7c990e18f539d03810cce374b1db76ecb7bce6840`。現場 `download.html` SHA-256 為 `ed24e782d405fa3310977fe75e524a6f8e18078b987a6177bdb23f531c97d7cb`，其原始候選位於 `/mnt/data-home/hungyu/multica-zh-tw/artifacts/hyclv159-operations/download-candidate.html`。這個靜態下載頁含特定 frontend CSS 雜湊，不能假定換版後仍可直接重用。

`.7` 發行資產保存在 `/mnt/data-home/hungyu/multica-zh-tw/artifacts/hyclv159-publish7/`，包含 `BUILD.txt`、`SHA256SUMS`、公開 `s90-ca.crt`、安裝器、四種 CLI archive 與 Apple Silicon App ZIP。`BUILD.txt` 標示來源 `d3ffd68c6d538b618dcafb6bd12bd1a9302522d8`。在該目錄執行 `sha256sum -c SHA256SUMS` 應全部通過，並核對持久卷內發布副本；不可只憑檔名推定版本。不要把私鑰或受保護設定加入鏡像。

## 後續重建與驗證

1. 從已審查的來源版本建立新發行資產，核對 `BUILD.txt`、平台成品、簽章與 `SHA256SUMS`。`scripts/lan-release/build.sh` 及 [內網安裝文件](lan-installation.zh-tw.md) 是安裝器的來源；新版本要重新核實公開 CA 和下載基址。
2. 以本 HTML 為起點更新版本與操作步驟。若改版後 Caddy 要維持獨立頁，將審查過的 HTML 和資產放進持久卷的版本化位置，準備精確路由與舊檔備份；正式寫入、Caddy reload 或 frontend 切換須另依核准方案執行。
3. 使用可信任的公開 CA 對正式 HTTPS 執行 GET，核對 `/download`、`/download/guide`、`/download/install.sh`、App ZIP、`SHA256SUMS` 的狀態、內容與雜湊。以 320、390、1440px 檢視指南，檢查鍵盤焦點和所有連結；真實安裝仍須另做人工驗收。

若新路由或內容驗證失敗，保留現場證據，依核准的發布備份恢復對應的 Caddyfile 內容和持久卷檔案，先 `caddy validate` 再 reload，重新核對公開端點。Caddyfile 是單檔 bind mount，恢復時保留 inode；有其他部署後不可整份覆蓋舊備份。本文件記錄目前可重建來源，不授權服務變更。

## 現行配套頻道與發布

最新程式契約見 [配套更新維護](unified-update.zh-tw.md)。
公開 HTML 是一般說明正本。生成的 `agent-update-guide.md` 是操作正本。
原 `download-update-guide.md` 不再公開。

有 App 時，App 一併更新內附 CLI 及 daemon。
無 App 時，由另一台實體機使用固定腳本更新。
Linux 使用原精確 systemd user 或 system unit。
舊版首次導入不中止中央 Server。維護交界漏接或中斷由使用者重新驅動。
常態更新仍要求支援閘門、排空及跨重啟保持。

頻道帶 `source_id=hyc5566/multica` 及 `coordinated_update_protocol=1`。
新版 App 使用 `coordinated_desktop`。舊 `desktop` 保持空物件。
這會阻止舊 helper 在缺少配套協調時自行換版。
舊 App 必須先通過明確的首次導入交易，才可使用新版頻道。
繁中版舊 `multica update` 直接 installer 路徑及 daemon 自動 installer 已停用。
`MULTICA_DAEMON_AUTO_UPDATE=false` 不控制 App 更新或停止領取。

### 建置

1. 核對單一來源 commit。通過條件：PR 已審查，來源已提交，工作樹乾淨。
2. 核對編譯器。通過條件：符合 `.github/RELEASING.md`，與本候選鎖定工具鏈一致。
3. 執行 `scripts/lan-release/build.sh VERSION NEW_OUTPUT HTTPS_IMMUTABLE_BASE VERIFIED_PUBLIC_CA`。
4. 在 Apple Silicon 使用同一 commit 建置 App。通過條件：內附 CLI 的完整 commit 與版號一致。
5. 把已驗證的 App ZIP 放入 `NEW_OUTPUT`。
6. 以 App 內附 CLI 作為同版 Darwin arm64 獨立成品。通過條件：兩者 SHA-256 完全相同。
7. 執行 `package-unified-update.py`。通過條件：產生固定來源的單檔腳本、各 Linux manifest、套件 metadata 及 `UPDATE-SHA256SUMS`。
8. 執行 `manifest.sh`。通過條件：App plist、內附 CLI build metadata 與配套清單一致。
9. 執行 `finalize-unified-release.py`。通過條件：必要資產全部存在，最終 `SHA256SUMS` 完整。
10. 執行 `node scripts/lan-release/stage-latest.mjs NEW_OUTPUT NEW_STAGE`。通過條件：重新核對全部 checksum，生成 HTML、指引及穩定下載檔名。

發行工具拒絕覆寫既有成品。不要把 `.3` 或其他舊成品當成本次新候選。
`BUILD.txt`、來源 commit、App 內附 CLI 及各平台 raw binary 都要獨立核對。
候選建置不表示已發布，也不表示實機驗收通過。

### 發布

1. 交候選、差異、完整 manifest 與隔離演練證據給首相。
2. 完成 Linux 腳本及一台 App 的整組換版與回滾實機驗證。
3. 取得首相放行。通過條件：適用門檻全部通過，原範圍核准不重問。
4. 核對正式路由及持久卷。通過條件：沒有其他部署衝突，回滾副本可用。
5. 將已驗證的固定版資產放入不可變版本目錄。
6. 執行 `sh scripts/lan-release/publish-latest.sh NEW_STAGE RELEASES_ROOT` 預覽。
7. 檢閱後以相同參數加 `--apply`。通過條件：文件、腳本與 checksum 先就緒，頻道最後切換。
8. 以可信 CA 驗證 HTTPS。通過條件：頁面、指引、腳本及 checksum 均為 200。
9. 核對 `.md` 的回應。通過條件：`Content-Type` 帶 `charset=utf-8`。
10. 核對實際下載 bytes。通過條件：腳本及配套 SHA-256 符合清單。
11. 從至少一台已導入的 App 檢查 latest。通過條件：收到同一核准候選。

Caddy 精確路由使用 `scripts/lan-release/Caddyfile.update-snippet`。
修改前保存差異及備份，保留原 TLS、代理路由與 bind mount inode。
先 validate，再 reload。新路由不授予服務變更權限。

### 失敗回復

任何步驟失敗立即停止後續發布。
依本次備份逐檔恢復文件、下載資產及頻道。
有其他部署前進時，只還原本次差異，不覆寫整份 Caddyfile。
回復後重驗 HTTPS、checksum、既有連線及 daemon 心跳。
頻道回退不會讓已下載的 App 自動降版。
客戶端只能回到本次交易的更新前版，並重驗身分、心跳及派工。
