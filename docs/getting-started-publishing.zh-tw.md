# 繁中版下載鏡像與獨立入門指南

最新發布契約見 [配套更新維護](unified-update.zh-tw.md)。新頻道使用 `coordinated_desktop` 並把舊 `desktop` 留空，避免舊 App 執行不協調的 swap。先以 `package-unified-update.py` 產生單檔腳本及固定 manifest，再產生頻道、HTML 和操作指引。

配套更新發布會生成 `agent-update-guide.md`，與頁面、腳本、checksum 及 latest 頻道同次發布。Caddy 精確路由來源是 `scripts/lan-release/Caddyfile.update-snippet`；套用前核對現場，validate 通過才可 reload。必須保留現有 TLS、代理路由及 bind mount inode。`.md` 回應要帶 `text/markdown; charset=utf-8`。實際發布仍須適用實機驗證及首相放行。

## s90 現行下載與指南入口

公開網址是 `https://10.1.24.90:45671`。下載頁由 Web 提供，App 僅列 macOS Apple Silicon；可用的獨立 CLI 平台及版本以線上下載頁與安裝腳本為準。

| 公開路徑 | 來源與用途 |
| --- | --- |
| `/download`、`/zh/download` | Web 下載頁 |
| `/downloads/selfhost/install.sh` | `scripts/install-selfhost.sh`；新機安裝器 |
| `/downloads/selfhost/guideline.md` | [selfhost-guideline.md](selfhost-guideline.md)；現行 CLI／daemon 安裝與更新指南 |
| `/downloads/selfhost/s90-ca.crt` | 經核對的公開 s90 CA |

舊安裝與舊指南路由已移除。文件、訊息及腳本中的入口都應更新至上表；不設相容轉址。
[入門頁](getting-started.zh-tw.html)只提供現行入口，不另複製安裝步驟與版本。
操作出錯時，先重新取得[最新版指南](https://10.1.24.90:45671/downloads/selfhost/guideline.md)，再依文件排查。

`selfhost/` 的正式主機目錄是 `/mnt/data-home/hungyu/multica-zh-tw/artifacts/hyclv100-downloads/selfhost/`，
透過 Web 的 downloads 目錄掛載提供，不在 Web image 內。指南可以獨立替換，不需重建或重啟 Web。

## 維護與驗證

1. 維護指南時，同步更新 `docs/selfhost-guideline.md` 與正式 `selfhost/guideline.md`。先保存舊內容與 SHA-256，在正式目錄寫入暫存檔後以 `mv` 原子替換。
2. 使用可信任的公開 CA 對上表每個 HTTPS 入口執行 GET。指南須回應 200、`Content-Type: text/markdown; charset=utf-8` 及 `Cache-Control: no-store`，讀回內容的 SHA-256 須符合來源。
3. 檢查兩個下載頁與指南的連結，只保留實際使用的入口。文件更新不代表授權替其他機台換版；跨機操作、備份、三次實際心跳、一次派工及失敗回復仍依指南執行。

正式 `Caddyfile` 位於 `/mnt/data-home/hungyu/multica-zh-tw/production/Caddyfile`。
修改前核對最新內容並備份差異，保留單檔 bind mount 的 inode，先 validate 再 reload。
失敗時只回復本次差異；有其他部署前進時，不得以整份舊備份覆蓋。
指南回復採同目錄原子換回備份，再從 HTTPS 讀回確認。本文件不授權其他服務變更。

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
