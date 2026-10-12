# 繁中版下載鏡像與獨立入門指南

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

## 發行頻道與自動更新（HYCLV-265 起）

自 `X.Y.Z-zh-tw.N` 正式版起，App 與 CLI 都讀同一份發行頻道 `https://10.1.24.90:45671/downloads/latest.json`：

| 對象 | 檢查時機 | 更新方式 |
| --- | --- | --- |
| macOS App（`src/main/zh-tw-app-update.ts`） | 開啟後 30 秒，之後每 24 小時 | 詢問使用者；同意後 App 自己下載 ZIP、核對大小／SHA-256、bundle ID、版本與 `codesign`，結束時保留 daemon 不停止（避免取消執行中的任務），替換 bundle 並重開；新版無法開啟時自動換回舊版。daemon 偵測到 App 內附的 CLI 已換版後，於閒置時在 claim barrier 下自行重啟成新版（`MULTICA_ZH_TW_DESKTOP_SELF_RELOAD`）；daemon 在 `/health` 回報 `zh_tw_self_reload: true` 時，App 不從外部停止它。是否支援以這個回報判斷，不看版本格式：0.4.43-zh-tw.7、0.6.0-131 等舊 daemon 不會回報，第一次手動換版時仍由 App 在 daemon 閒置時重啟（下載頁的升級步驟另請使用者確認 CLI 版本）。前一版留在 `~/Library/Application Support/Multica 繁中版/zh-tw-update/rollback/` |
| CLI daemon（launcher 安裝） | daemon 啟動 2 分鐘後，之後每 24 小時 | 閒置時下載頻道指定且 SHA-256 相符的 `install.sh`，執行 `--upgrade`；launcher 原子切換到新版本目錄，daemon 經 launcher 重新啟動（由 systemd 管理時改為結束程序，交給 `Restart=on-failure` 以新版重啟）；保留前一版本目錄 |

固定安裝指令是 `curl -fsSL https://github.com/hyc5566/multica/releases/latest/download/install.sh | bash`，因此每個正式版都要在 GitHub 發布為 **Latest**，並附上該版 `install.sh`。只有 `X.Y.Z-zh-tw.N` 會進入頻道；`-rc.N` 與 git-describe 版本不會被自動更新。

### 發布順序

1. `scripts/lan-release/build.sh VERSION OUT https://10.1.24.90:45671/downloads/VERSION CA` 建置 CLI 與安裝器；App 在 m5 以 `scripts/build-desktop-zh-tw-macos.sh` 用**同一個 commit** 建置，ZIP 放進 `OUT`。
2. `scripts/lan-release/manifest.sh OUT https://10.1.24.90:45671/downloads/VERSION <GitHub 版本說明 URL> OUT/multica-desktop-VERSION-mac-arm64.zip` 產生 `latest.json`；`scripts/lan-release/render-download-page.sh OUT OUT/download.html` 產生下載頁。重新產生 `SHA256SUMS` 把兩者納入。
3. 把 `OUT` 複製到 Caddy 持久卷 `/config/releases/VERSION/`，用公開 CA 對 `https://10.1.24.90:45671/downloads/VERSION/` 下所有檔案 GET 並比對 SHA-256。
4. 發布 GitHub Release `zh-tw-vVERSION`（Latest），資產含 `install.sh`、CLI archives、`s90-ca.crt`、App ZIP、`SHA256SUMS`。
5. 執行 `node scripts/lan-release/stage-latest.mjs OUT NEW_STAGE`，核對來源 `SHA256SUMS`、App 大小／雜湊、installer 雜湊與 `BUILD.txt`。工具把安裝檔複製到全新目錄，將 App 改名為 `multica-desktop-latest-mac-arm64.zip`，重新產生對應檔名的 `SHA256SUMS` 與下載頁。既有輸出目錄會被拒絕；驗證失敗不發布。
6. 最後才切換頻道：先執行 `bash scripts/lan-release/publish-latest.sh NEW_STAGE RELEASES_ROOT` 預覽，正式發布經核准後加 `--apply`。`RELEASES_ROOT` 是 Caddy 的 `/config/releases` 持久卷；工具需在能存取該目錄的環境執行，容器內沒有 Node 時先在主機 stage，再把候選目錄和 shell 工具複製進容器。工具先備份原有 `latest/`、`latest.json`、`download.html`，再複製覆蓋 `latest/` 的安裝檔，每個檔案先寫暫存檔再 `mv`；檢查碼驗證完成後才切換根目錄的頻道與頁面。任何步驟失敗立即停止，依印出的備份路徑回復；不繼續發布其他元件。

下載頁只有頂部顯示目前版本；App、SHA256SUMS 與版本說明都使用固定的 `latest` 連結。每次更新都必須執行第 5、6 步，不能只上傳版本化檔案。`latest/SHA256SUMS` 會列出實際的 latest App 檔名。自動更新頻道的 `latest.json` 仍保留版本化 App／installer URL，使已開始的更新下載固定在同一版；請勿把頻道內的 URL 改為可被覆蓋的路徑。

發布後對 `/download`、`/downloads/latest/multica-desktop-latest-mac-arm64.zip`、`/downloads/latest/SHA256SUMS` 執行 HTTPS GET（使用可信 CA），比對頁面、實際下載檔名與 SHA-256，並確認版本化 URL 仍可下載。多檔更新期間，手動下載可能碰到短暫的檢查碼差異，應重新取得 App 與 SHA256SUMS；不可略過驗證。回復時先把備份的安裝檔逐檔以暫存檔加 `mv` 還原，再還原 `latest.json` 與 `download.html`，重新 GET 驗證。首次發布前没有 latest 目錄時，回復舊頁與頻道即可停止提供新連結；不必刪除已驗證的副本。

### Caddy 路由（一次設定，之後換版不改 Caddyfile）

```caddy
@zhtw_latest path /downloads/latest/*
handle @zhtw_latest {
	uri strip_prefix /downloads/latest
	root * /config/releases/latest
	header Cache-Control "no-store"
	file_server
}
@zhtw_channel path /downloads/latest.json
handle @zhtw_channel {
	root * /config/releases
	rewrite * /latest.json
	header Cache-Control "no-store"
	file_server
}
@zhtw_release path_regexp ^/downloads/[0-9]+\.[0-9]+\.[0-9]+-zh-tw\.[0-9]+/
handle @zhtw_release {
	uri strip_prefix /downloads
	root * /config/releases
	file_server
}
@zhtw_download path /download /download/
handle @zhtw_download {
	root * /config/releases
	rewrite * /download.html
	header Cache-Control "no-store"
	file_server
}
```

新路由取代現有 `@download_435`；`/download/install.sh` 改為 `redir https://github.com/hyc5566/multica/releases/latest/download/install.sh 302`。既有 `/downloads/0.4.43-zh-tw.*` 路由保留給舊安裝。修改正式 Caddyfile 與持久卷仍依 `docs/server-handoff.zh-tw.md` 及核准方案執行。

### 舊版過渡

0.4.x 與 `0.6.0-131-g2ca7196c3b` 等舊版不含更新程式，需手動升級一次：App 下載新版取代；CLI 執行 `curl -fsSL …/install.sh | bash -s -- --upgrade` 後重啟 daemon。舊 launcher 關閉自動重載，升級後必須重啟 daemon 才會換版。
