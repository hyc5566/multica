# Multica 繁中版：安裝、驗收與發行

主要維護位置為本文件；操作結果另記於 s90 的維運文件。來源為 `hyc5566/multica` 的 `zh-tw`，所有正式成品必須對應同一個已驗收 commit。合併程式、建立成品、發布 Release、更新正在運行的 Server 是不同步驟。

## 使用模型

每人一個 Multica Server 帳號，並在每台機器使用自己的 Linux／macOS 帳號。同一人可在 Mac、v6、s234、s90 各有一個 daemon；一個 daemon 可提供多個 Agent 工具。Server Workspace 是成員／議題／權限的協作範圍，本機工作目錄則存放任務執行檔案。

| 路徑 | 新 CLI 安裝用途 |
| --- | --- |
| `~/.local/bin/multica` | 啟動入口，固定版本、Server URL 與 CA |
| `~/.local/share/multica-zh-tw/<version>/` | Binary、授權文件、公開 CA 與 launcher |
| `~/.multica/config.json` | 使用者預設設定與 token |
| `~/.multica/workspaces/` | 任務工作目錄 |

安裝器不建立具名 profile；profile 是進階的獨立連線設定，不是人格或 OS 帳號。Desktop 內部仍管理獨立 profile，使用者不用輸入名稱。各機器使用獨立 daemon 身分，建議各有可單獨撤銷的 token；不可跨機共用整份可寫的 `.multica`，包括共享家目錄情境。

既有 hungyu 的 v6 使用 `/mnt/data-home/hungyu/multica-zh-tw/v6/bin/multica --profile v6`；s90 使用 `/mnt/data-home/hungyu/multica-zh-tw/production/bin/multica --profile s90`。新安裝器不自動遷移這些既有安裝。

## Mac App

1. 從 GitHub Release 下載 `multica-desktop-<version>-mac-arm64.zip`，核對 SHA-256，解壓縮後安裝 App。
2. 預設連到 `https://10.1.24.90:45671`，繁體中文介面保留英文 Agent 用語，可切換英文。
3. App 隨包提供公開 CA：Node 主程序、Chromium 及內嵌 Go CLI 都處理 LAN 信任，保留公用 CA，不修改系統信任、不關閉 TLS 驗證。Chromium 的憑證例外僅限固定 s90 入口，另外以 Node TLS 完整驗證鏈／主機名／效期並比對同一 leaf。
4. 使用自己的 Email 收取一次性驗證碼，建立／加入自己的工作區。15 分鐘效期需要搭配本次 Server 版本與已設定寄信服務；舊 Server 不會因安裝新版 App 自動更新。
5. 設定中手動啟動 daemon。使用 App 內嵌同版本 CLI，不需要 terminal PATH；不回退下載官方 CLI。
6. 偵測本機 Codex、Claude Code、Antigravity（agy）、Hermes；AI 工具須各自安裝與登入，測試一個實際 Agent 議題。
7. 刪除目前 Desktop token 時提供重新產生確認：先建立新 token、讓 daemon 使用並驗證，再撤銷舊 token。取消保留原狀；有執行中任務或無法確認 daemon 狀態時不強制輪替。失敗顯示原因、保留可恢復狀態。

### Desktop 每機器憑證歸屬

Desktop 每個 OS 帳號以本機安裝 UUID 與 hostname 區分憑證歸屬，連線 profile 仍依 Server host 分開。相同機器、相同 OS 帳號與 Server 的 App 共用同一個受管理 daemon；本設計不新增另一份 runtime。

新簽發的 PAT 名稱包含 hostname，僅供辨識。程式保存完整 token ID、SHA-256 摘要、使用者、Server URL 與本機歸屬，重用及輪替不以名稱或短 prefix 判斷。即使兩台登入時輸入同一枚 PAT，也只用它授權各自建立新的本機 PAT，不直接把輸入值當成 daemon 憑證。

升級前的快取沒有歸屬證明：首次同步以有效 App 登入換發本機 PAT，保留舊 PAT 在 Server 上的狀態，不自動撤銷可能被其他機器使用的憑證。簽發失敗時保留原設定並回報錯誤；寫入中途失敗則可能已保存新憑證，重試時以歸屬資料重新檢查，不能保證回復舊憑證。確認各機器已換發後，使用者才可自行清理不再使用的舊 token。不要跨機複製整份可寫的 `.multica`；hostname 改名也會觸發保守換發。

重建時保留 `apps/desktop/src/main/desktop-token.ts`、`daemon-manager.ts` 的歸屬驗證與輪替整合，以及相鄰 Desktop 測試。後端不需 schema 變更；`server/internal/handler/desktop_machine_isolation_test.go` 以真實認證及隔離 PostgreSQL 驗證兩台註冊、心跳及獨立續期／撤銷，不涵蓋 Redis 跨節點失效或真人操作驗收。

第一批 Mac 目標為 Apple Silicon。ad-hoc 簽章不等於 Apple Developer ID／公證，未完成 Gatekeeper 驗收不得宣稱一般下載即可無提示開啟。Intel 未實機驗收前不列為穩定支援。

## Linux 無 App 安裝

先從 Web／App 建立自己的個人 API token。遠端不需要 App，也不需要開 GUI 登入。

從固定 GitHub Release 的 HTTPS URL 下載 `install.sh`，先檢查來源與內容再執行（以下 VERSION 必須替換為已發布版本）：

```bash
curl --fail --location --proto '=https' --proto-redir '=https' \
  'https://github.com/hyc5566/multica/releases/download/zh-tw-vVERSION/install.sh' \
  -o install.sh
bash install.sh --login
```

`--login` 提示輸入 token，驗證帳號後啟動 daemon；不把 token 寫在 shell history。單純 `bash install.sh` 只安裝，之後使用：

```bash
"$HOME/.local/bin/multica" login --token
"$HOME/.local/bin/multica" daemon start
"$HOME/.local/bin/multica" daemon status
"$HOME/.local/bin/multica" daemon stop
```

需要 systemd 使用者服務時改用 `bash install.sh --service`，它包含登入、建立／啟用 `multica.service` 與連線檢查。必須有可用的 systemd user manager；登出後持續運行所需 linger 由機器管理員依政策開通。服務管理：

```bash
systemctl --user status multica.service
journalctl --user -u multica.service
systemctl --user restart multica.service
systemctl --user disable --now multica.service
```

不要同時以 systemd 與手動指令啟動兩份服務。動態埠模式由 OS 分配可用的 loopback 埠，以使用者狀態檔發現 port/PID，檢查 daemon 身分並用生命週期鎖避免同帳號重複啟動；不要求使用者命名 profile。舊具名 profile／Desktop 的既有埠契約保持相容。

安裝器會拒絕既有 launcher、版本目錄或預設設定，不靜默覆寫。尚未加入 shell PATH 時直接使用上述完整路徑；可自行將 `~/.local/bin` 加入 PATH。

## TLS 與 Server URL

一般同事使用 `https://10.1.24.90:45671`；s90 舊 daemon 的 `http://127.0.0.1:45673` 是同機直接連 backend，不能照抄到其他主機。安裝 launcher 固定一般入口，並把 OS 公用 CA 與隨包 s90 公開 CA 合併後提供給 CLI 及子程序。

### 安裝時出現 curl (60)

2026-09-17 起的安裝腳本會在下載 CLI **之前**，合併系統公用 CA、既有 `CURL_CA_BUNDLE`／`SSL_CERT_FILE` 指向的公開憑證，再明確指定下載與 HTTPS proxy 使用這份信任。只有內網 CA 的舊環境設定不再遮蔽 GitHub 所需的公用 CA。安裝後 launcher 同時設定這兩個變數，使用包含公用與 s90 CA 的憑證包；不修改系統信任，不關閉 TLS 驗證。請重新下載腳本，舊的本機副本不會自動更新。

若公司代理需要額外的 CA，先向管理員取得並核實**公開 CA 憑證**，再執行：

```bash
MULTICA_INSTALL_CA_FILE=/absolute/company-public-ca.pem bash install.sh --login
```

這份公開 CA 也會加入安裝後的憑證包。不可提供私鑰；檔案不存在、沒有系統 CA、憑證不受信任或主機名稱不符時，安裝仍會停止。macOS 鑰匙圈中的額外公司 CA 不保證包含在系統 PEM 檔，必要時也用上述方式提供。

若錯誤發生在**下載 install.sh 本身**，腳本尚未開始，無法自行修復第一個下載。可用已信任 GitHub 的瀏覽器下載後傳至目標機器。Debian／Ubuntu 類 Linux 若只是繼承到錯誤的 CA 環境變數，也可明確使用系統 CA 重新下載：

```bash
env -u CURL_CA_BUNDLE -u SSL_CERT_FILE -u SSL_CERT_DIR \
  curl -q --cacert /etc/ssl/certs/ca-certificates.crt \
  --proxy-cacert /etc/ssl/certs/ca-certificates.crt \
  --fail --location --proto '=https' --proto-redir '=https' \
  https://raw.githubusercontent.com/hyc5566/multica/zh-tw/scripts/install-zh-tw.sh \
  -o install.sh
bash install.sh --login
```

若公司代理 CA 尚未受信任，上述下載仍會拒絕連線；應使用管理員核實、包含系統與公司公開 CA 的憑證包取代兩個 `--cacert`／`--proxy-cacert` 路徑。此修正的自動測試使用真正的 HTTPS 連線，檢查成功安裝、未知 CA、主機名稱不符與私鑰輸入；不代表 Desktop 或各 Agent 工具的完整 CA 相容性驗收已通過。

首次下載來源必須已受信任，例如 GitHub HTTPS；不可使用 `curl -k` 從尚未信任的 LAN 網站抓 CA 後當成已驗證。公開根憑證與 binary 同屬固定 checksum 的包；私鑰不可分發。一般瀏覽器的 Web 入口仍需要系統／瀏覽器信任 CA，或由管理員提供已受信任的服務憑證；App 的內建信任不會改變瀏覽器。

`setup` 對同 Server 保留既有設定與 token，驗證成功後跳過重登；換 Server 不沿用舊 token／workspace。TLS／網路失敗不再當成 token 過期，HTTP 401 才提示登入。

## 寄信與新使用者驗收

管理員須設定公司 SMTP 或既有 Resend；`SMTP_HOST`、`SMTP_PORT`、`SMTP_USERNAME`、`SMTP_PASSWORD`、`SMTP_FROM_EMAIL`、`SMTP_TLS` 使用既有設定。密碼只進安全設定管道。驗證碼 15 分鐘有效、使用後失效，重寄／錯誤次數／過期均須驗證。

不可公開 `show-login-code.sh` 或提供輸入任意 Email 的查碼頁。沒有寄信設定時，不能宣稱 Email 自助註冊已上線。

## 建置與發布

先整理測試及人工檢查結果，再以正常 merge／fast-forward 將候選整合到 `zh-tw`。所有成品從該 commit 建置；不改寫共用歷史，不混入無關上游升級。使用 `zh-tw-v<version>` tag，避免觸發上游 `v*` 發行流程或更新官方 Homebrew。

Linux 建置（Go 依 `server/go.mod`）：

```bash
bash scripts/lan-release/build.sh 0.4.41-zh-tw.6 \
  /absolute/new/release-directory \
  https://github.com/hyc5566/multica/releases/download/zh-tw-v0.4.41-zh-tw.6 \
  /absolute/verified-public-s90-ca.crt
```

產出四種 CLI archive、Server/migrator、來源包、安裝器、BUILD.txt 及 SHA256SUMS。交叉編譯不等於各架構實機驗收。

Apple Silicon 原生建置（Node 22–24、pnpm lockfile、Go、Xcode CLI tools）：

```bash
MULTICA_ZH_TW_CA_FILE=/absolute/verified-public-s90-ca.crt \
  bash scripts/build-desktop-zh-tw-macos.sh . 0.4.41-zh-tw.6
```

內嵌 CLI 強制同版本，不容許缺 Go 時回退到官方下載。保存 App 簽章／公證與 Gatekeeper 證據，再把 Mac ZIP 與校驗碼加入同一 Release。先建 draft 核對下載內容與版本，必要驗收未完成時可維持 draft，或明確標為可供真人驗收的 pre-release，不能標為穩定版。

## 驗收與回復

發行前：相關 Go／Desktop／UI 測試；全新 Linux 安裝；同機兩個使用者隔離；新 Mac 首次登入、手動啟動、實際 Agent 任務；token 輪替與恢復；真實信箱領碼；版本／checksum 一致。完整新使用者流程與單元測試分開記錄。

升級前先確認沒有執行中任務，備份 launcher、既有 App、設定及 token（限制權限），保留前版成品。新版 binary 放不同版本目錄；在相同狀態根目錄與服務管理方式下切換 launcher。失敗停止新 daemon、恢復舊 launcher／App 與必要設定，再核對登入、註冊與 heartbeat。不要全域依程序名稱 kill。

Server 部署前另核對正在運行的版本、images、設定／資料備份與相容性。此次 OTP 修改沒有新 schema migration；仍須以實際合併版本的 schema 檢查為準。切回 binary 不等於恢復資料。未核准正式服務更新前，發布 GitHub 成品不會自動變更 s90 服務。

## 2026-09-11 交付狀態

[0.4.41-zh-tw.6](https://github.com/hyc5566/multica/releases/tag/zh-tw-v0.4.41-zh-tw.6) 為公開內網驗收版本。App 僅 Apple Silicon；CLI 可直接使用 repository 的 `scripts/install-zh-tw.sh`，或下載同版 Release 的 `install.sh`，兩者內容相同。既有安裝仍依上方升級步驟保留設定。

s90 Server／Web 已部署來源 `aa863a493426d4f6d8df326e18ece5bc7d82ec95`；Gmail 寄信與新驗證碼 15 分鐘期限通過。共享 Redis 與雙入口代理已啟用；操作以 [Server 交接程序](server-handoff.zh-tw.md) 為準。

Mac 原生 651 項測試、內嵌 CA 連線、codesign、新舊 Server 混版 HTTP／登入 WebSocket／Redis 演練通過。正式穩定版仍待新使用者實際完成 Mac 註冊登入→Agent 任務，以及 Linux systemd 常駐→領取任務；不以編譯通過代替這些驗收。
