# 繁中 App 的 CA 自動更新

## 範圍

App 啟動時及每小時讀取固定的公開清單：

`https://raw.githubusercontent.com/hyc5566/multica/zh-tw/config/s90-ca-manifest.json`

清單透過 Node 內建公用根憑證驗證 HTTPS，不使用待輪替的內網 CA，不傳登入 token，也不接受轉址。這個 GitHub repository／分支的寫入權即為 CA 發布權，應依既有分支審查權限管理。本機程式碼存在不代表清單已發布；必須先核准並發布清單，再發布含此功能的 App／內嵌 CLI。

其他 Multica 帳號不需各自設定。每個作業系統使用者的快取仍獨立；首次啟動最多等待 8 秒檢查清單後才開始登入連線。離線時沿用已驗證快取；沒有快取時使用 App 內附 CA。獨立安裝的 CLI 不會讀取其他使用者的 App 快取，本功能只自動管理此 App 與它啟動的 daemon／任務 CLI。

## 清單格式與驗證

`config/s90-ca-manifest.json` 包含：

- `version`：遞增正整數；同版本內容不得變更，舊版本不得覆蓋本機已接受版本。
- `expiresAt`：UTC ISO 時間；下載到過期清單時拒絕更新。已接受快取可在清單到期後繼續供離線使用，但不使用已過期的根憑證。
- `certificates`：1–4 個 PEM 公開根憑證。僅接受目前有效、CA=true、自我簽署驗證成功且不重複的憑證；拒絕私鑰及非憑證資料。HTTP 回應最多 64 KiB。

已接受的清單是 App 管理之內網 CA 的權威集合；不會永遠保留被清單移除的原始 bundled CA。作業系統／Node 的其他既有預設信任根維持不變，清單無法撤銷使用者另外安裝到系統的 CA。快取中某個舊根到期時，不會因此丟棄仍有效的新根或最高已接受版本。

App 將 bundle／清單寫入當前使用者的 `userData`，先準備兩份暫存檔，再用 rename 提交，並用 hard link 保留舊 bundle。更新套用失敗時嘗試透過 rename 恢復先前 bundle 與 Node 信任池，不提交新版本清單；無法還原時保留備份與錯誤日誌。下載、驗證或寫入錯誤只記錄不含機密的日誌，不停用 TLS 驗證。

## daemon 安全套用

CA bundle 固定為 `userData/ca-bundle.crt`，透過 `MULTICA_CA_CERT_FILE` 傳給內嵌 CLI。App 啟動後也會核對存活 daemon，避免上次 App 離開時留下尚未套用的更新。

新版 daemon 使用 loopback `POST /restart/idle`；App 提供預期 PID、CA 路徑與 bundle SHA256。daemon 比對真正啟動時載入的 hash：相同即不需重啟；不同時驗證磁碟內容並取得原子的 task claim barrier，確認沒有任務／正在領取工作才進行既有 self-restart。這不使用有強制停止備援的 CLI stop。

App 在任務計數未知、有任務、外部／WSL daemon、profile／Server不符、舊版daemon回404、憑證路徑不符或更新失敗時保留待套用狀態，後續輪詢重試，不強制中斷任務。若daemon仍使用手動指定的其他憑證路徑，需在部署時讓App正常啟動它，改用固定bundle；本機更新流程不越權改寫陌生profile或程序。

## 輪替程序

1. 先確認所有目標 App／daemon 支援此功能，且使用者可連到固定 GitHub 來源。舊 App 不會因清單發布而自動獲得新程式。
2. 清單先同時發布舊、新根，提高 `version`、更新有效期限；不得修改已發布版本而不升版。
3. 等候目標使用者完成下載與daemon安全套用，包含曾離線／睡眠的機器。只有Server在線或經過一小時不能證明全部已更新。
4. 再依部署核准切換Server憑證；驗證App登入、HTTP、WebSocket與daemon心跳。
5. 完成過渡後，以更高版本清單移除舊根。回復也用更高版本重新發布已核實的舊根，不降低版本號。根被撤銷後，已建立的連線可能持續到重新連線；不要將清單更新宣稱為瞬間撤銷所有既有TLS連線。

若新根導致問題，Server先維持／恢復仍受信任的有效憑證，再發布更高版本清單修正。此清單沒有任意指令、執行檔、URL或私鑰欄位，不是App二進位自動更新器。

## 驗證入口

Desktop：`pnpm --filter @multica/desktop test`、`pnpm --filter @multica/desktop typecheck`。
Go：使用 `scripts/go-test-with-agent-cli-guard.sh` 執行 `internal/cli` 憑證與 `internal/daemon` idle restart／claim barrier相關測試。
回歸涵蓋下載限額／逾時／拒絕轉址、格式及CA驗證、版本降級、離線／快取到期、寫入失敗恢復、首次啟動等待、根撤換與安全重啟；正式發布仍需原生App及實際新舊CA過渡驗收。
