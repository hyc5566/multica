# 繁中版下載鏡像與獨立入門指南

主要內容來源是 [getting-started.zh-tw.html](getting-started.zh-tw.html)。這份 HTML 不需要 JavaScript 或外部字型，可單獨開啟。頁面目前說明 `0.4.43-zh-tw.7`；發布新版本前須更新版本、平台、安裝器、CA 與連結，再重新驗證。

## 目前 s90 發布狀態

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
