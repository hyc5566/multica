# 繁中版：公司內網安裝與共用 Server

這份文件隨 HYCLV-64 候選版保存。候選驗證不代表已部署或已完成人工驗收。s90 的已知入口是 `https://10.1.24.90:45671`；正式 Server/Web 基準為 `f37db6872`／`0.4.41-zh-tw.2`。本次 release 從遠端 `zh-tw` 納入正式版本的 6 個既有提交，保留用量與 Desktop 瘦身功能。

## 1. 管理員先備妥

- 一組固定版本 Server/Web images、同來源 CLI、`BUILD.txt`、`SHA256SUMS` 與本文件。不可用官方 `latest` 取代繁中版。
- 同事可連線的 HTTPS 入口、受信任憑證、必要網段規則。不要開放 raw API、Web 或 PostgreSQL 到外網。
- 每人使用自己的 OS 帳號與公司 Email。確認 daemon 執行機器的擁有人同意提供工作目錄、工具與資源。
- 身分驗證必須先選定：公司 SMTP／Resend 寄送一次性驗證碼，或另外實作管理員核發的個人一次性邀請領碼。後者在本候選版尚未實作。

現有 `show-login-code.sh` 僅供管理員；不要把它直接包成可輸入任意 Email 的網站。那會繞過信箱持有驗證。它也只查最新一筆，可能已用過或已過期，不能將其輸出一律視為 unused。

## 2. 同事登入

1. 由管理員提供 Server URL、CA 根憑證與經另外管道核對的 SHA-256 fingerprint。根憑證是公開資料，私鑰不可分發。
2. 將 CA 加入該機器的系統信任（須機器管理員核准）。macOS 使用「鑰匙圈存取」；Debian/Ubuntu 將核對過的 `.crt` 放入 `/usr/local/share/ca-certificates/` 後執行 `sudo update-ca-certificates`。不要使用 `curl -k` 或關閉 TLS 驗證。
3. 執行 `curl --fail https://10.1.24.90:45671/health`，確認為預期版本。只在這一步成功後安裝及登入。
4. 開啟 `/login`，輸入自己的公司 Email，從自己的信箱取得一次性碼。尚未設定寄信時，這個自助流程不可宣稱可用，管理員須透過已確認身分的私下管道協助。
5. 首次登入建立個人帳號。若需共用團隊，工作區管理員從成員介面邀請其加入；Server 帳號存在不代表有權存取其他工作區。

SMTP 使用現有 `SMTP_HOST`、`SMTP_PORT`、`SMTP_USERNAME`、`SMTP_PASSWORD`、`SMTP_FROM_EMAIL`、`SMTP_TLS`；一般 STARTTLS 用 587，implicit TLS 用 465 並設 `SMTP_TLS=implicit`。保持 `SMTP_TLS_INSECURE=false`、`APP_ENV=production`，不設定固定 `MULTICA_DEV_VERIFICATION_CODE`。可用 `ALLOWED_EMAIL_DOMAINS` 限定公司網域，但網域白名單本身不是身分驗證。實際寄信須使用核准的測試收件人，紀錄寄送／領碼／一次性消耗結果，不保存驗證碼。

## 3. 在指定機器安裝 daemon

支援的 CLI 成品為 Linux amd64/arm64、macOS Intel/Apple Silicon；交叉編譯不等於每個 OS 已通過實機驗證。Windows 及新的 macOS Desktop App 不包含在這份 CLI 安裝包中。

1. 登入 Web 或相同候選版 Desktop，切換正確工作區，開啟「執行環境 → 連接其他機器」。
2. 在**要執行任務的機器**，以同事自己的 OS 帳號執行第一步安裝命令。管理員設定 `MULTICA_DAEMON_INSTALL_URL` 後，此步改為下載固定版本的內網安裝器。
3. 安裝器驗證內嵌的 SHA-256，binary 放入 `~/.local/share/multica-zh-tw/<version>/multica`；啟動入口是 `~/.local/bin/multica`。既有安裝會拒絕覆寫，不帶 --login 時只安裝；帶 --login 時依提示登入並啟動 daemon。
4. 執行介面第二步，或使用：

   ```bash
   "$HOME/.local/bin/multica" login --token
   "$HOME/.local/bin/multica" daemon start
   ```

   依提示完成自己的登入及工作區選擇。若已有該 profile，setup 可能重新啟動 daemon；先確認沒有進行中的任務。不要使用別人的 API token。
5. wrapper 不指定具名 profile，使用目前 OS 帳號的預設設定，停用官方自動更新及自動重新載入，避免繁中 binary 被取代。它使用 default 設定；同一 OS 帳號已有 default 設定時須先核對，不可當成另一套隔離設定。需要第二組環境時可自行指定 --profile。
6. 在指定機器查核：

   ```bash
   "$HOME/.local/bin/multica" version
   "$HOME/.local/bin/multica" daemon status --output json
   "$HOME/.local/bin/multica" daemon logs
   ```

   核對 Server URL、profile、版本、daemon 身分、工作區與近期 heartbeat；介面「已連線」提示不足以證明是本次機器。再在 Web 選擇該執行環境，執行一個已核准的簡單議題確認結果。
7. 在此 OS 帳號安裝並登入所需 AI CLI。工具授權與配額屬於執行機器的帳號；加入工作區的成員可能透過 agent 使用它們。需要不同信任邊界時分開 OS 帳號／機器／工作區，不把工作區當作 OS 沙箱。

### Linux 開機常駐（機器擁有人核准後）

先停掉同 profile 的手動 daemon，再以 user service 管理，勿同時啟動兩份：

```ini
[Unit]
Description=Multica zh-TW personal daemon
After=network-online.target

[Service]
ExecStart=%h/.local/bin/multica daemon start --foreground
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
```

存為 `~/.config/systemd/user/multica-zh-tw.service`，執行 `systemctl --user daemon-reload` 及 `systemctl --user enable --now multica-zh-tw.service`。登出後需繼續運作時，由管理員核准並啟用該帳號的 linger，再確認 `loginctl show-user <帳號> -p Linger`。停用用 `systemctl --user disable --now multica-zh-tw.service`。macOS 可由同來源 Desktop 管理其自己的 daemon，或另行核准 LaunchAgent；不要重複管理同一 profile。

## 4. 新建共用 Server

以下是**新環境**流程；不是可直接覆蓋 s90 production 的指令。

1. 使用 Docker Compose v2，取得候選 `source.tar.gz` 與已建置且已核對 SHA-256 的 images，`docker load -i <images.tar>`。原始碼解壓到獨立目錄。
2. 將 root `docker-compose.selfhost.yml`、`deploy/lan/compose.yml` 及 `deploy/lan/Caddyfile` 複製到新的部署目錄。Compose 路徑相對第一份 compose 檔案，Caddyfile 必須同層。
3. 從 `.env.example` 建立權限 0600 的部署 `.env`，設定獨立隨機 `JWT_SECRET` 與資料庫密碼（妥善保存，勿輸出到議題）。設定：

   ```dotenv
   MULTICA_BACKEND_IMAGE=multica-zh-tw-backend
   MULTICA_WEB_IMAGE=multica-zh-tw-web
   MULTICA_IMAGE_TAG=<審查過的候選完整 commit>
   MULTICA_RELEASE_VERSION=<BUILD.txt 中的 version>
   MULTICA_RELEASES_DIR=/srv/multica/releases
   LAN_BIND_IP=<這台 Server 的內網 IP>
   LAN_HTTPS_PORT=45671
   MULTICA_APP_URL=https://<這台 Server 的內網 IP>:45671
   MULTICA_PUBLIC_URL=https://<這台 Server 的內網 IP>:45671
   MULTICA_DAEMON_SERVER_URL=https://<這台 Server 的內網 IP>:45671
   FRONTEND_ORIGIN=https://<這台 Server 的內網 IP>:45671
   BACKEND_PORT=45673
   FRONTEND_PORT=45672
   APP_ENV=production
   ```

   這些埠需要未被占用；host 變動須重新產生對應 base URL 的安裝器，不能沿用指向 s90 的安裝器。
4. 在 release 目錄的版本子目錄只放可公開下載的 CLI archives、`install.sh`、checksum 與文件。不可放 `.env`、備份、profile、Server logs、金鑰。不要公開整個 staging 根目錄。
5. 完成 Email 設定與工作區政策後，以明確 project name 啟動：

   ```bash
   docker compose --project-name multica-company --env-file .env \
     -f docker-compose.selfhost.yml -f compose.yml config --quiet
   docker compose --project-name multica-company --env-file .env \
     -f docker-compose.selfhost.yml -f compose.yml up -d --no-build
   ```

6. 取出該環境 Caddy 公開根憑證，核對 fingerprint，交給同事信任。驗證 health commit、Web 登入、寄信收碼、WebSocket 握手與維持、daemon registration/heartbeat、成員隔離、重啟持久性；保留時間與實際版本。未驗證者不得標示部署成功。

## 5. 更新、備份與回復

更新前停止接新任務，確認所有相關 daemon 無 active task，記下 images ID、部署 `.env`、Caddyfile 與 compose，備份 PostgreSQL、uploads 及必要 CA/config volumes。備份必須限制權限並驗證可讀。程式回復與資料恢復是不同操作。

本候選相對 s90 `f37db6872` 沒有新增 migration。Server/Web 回復可使用既有 images；新登入服務未上線，不涉及正式登入資料變更。若日後有 migration，必須重新評估 schema 相容性，不能沿用這個判斷。

對既有 s90：保留原 production compose、`.env`、Caddyfile 與 image ID。新增下載目錄 mount 和安裝 URL 是正式設定變更，須核准後才做。回復時還原這三份設定，使用原 images 執行同 project 的 `up -d --no-build backend frontend caddy`，重新驗證 health、登入、WebSocket、daemon registration/heartbeat。資料庫與 uploads 不因切回 images 自動恢復，也不要隨意刪 volume。

daemon 升級先核對 profile 並停止該服務，保存舊 launcher，將新版放入新的版本目錄；只在核准後切換 wrapper。驗證失敗切回舊 wrapper 並啟動，同樣重新驗證。不要以全域名稱 kill `multica`。

## 6. 重建

根規範要求 main 只追蹤上游，因此此繁中候選使用本機分支與 `-zh-tw.*-rc.*` 版本，不為本候選新增上游 main release tag。正式發行策略需在發布前確認。

在乾淨且已提交的 Linux checkout，使用 lockfile 與 Go module 指定版本：

```bash
bash scripts/lan-release/build.sh 0.4.41-zh-tw.4-rc.1 \
  /absolute/new/release-directory \
  https://10.1.24.90:45671/downloads/0.4.41-zh-tw.4-rc.1
```

此命令只建置本機 CLI、Server、migrator、來源包；Web image 依 `Dockerfile.web`，Backend image 依 `Dockerfile`，以同一 source commit 建置並存檔，不在正式發布時重新 build。Server Linux 包需要 PostgreSQL，先執行 migrate 才執行 server；一般部署使用現有 Docker entrypoint 自動 migration。

繁中安裝擴充集中在 config API 的 `daemon_install_url`、shared config store 與「連接其他機器」dialog；未設定時沿用上游行為。重建繁中版時保留這個 runtime config 邊界與驗證 URL 的測試，不改寫登入的信任模型。原有繁中功能清單見 [zh-tw-maintenance.md](zh-tw-maintenance.md)。

## Desktop 手動啟動與憑證恢復（候選功能）

新設定預設關閉自動啟動；已有設定會保留原選擇。既有使用者可在「設定 → 守護程序」關閉自動啟動，再按「啟動」。這會使用 App 目前登入的帳號同步憑證，並呼叫 App 內嵌 CLI，不需要 terminal PATH 有 multica，也不需要手動指定 Desktop profile。

若 desktop API token 被刪除，App session 仍有效時，啟動前會先確認 cached token；Server 明確回覆 401 才補發。網路失敗或 Server 暫時異常不視為 token 撤銷，也不會因此登出 App。若 App session 本身也過期，需重新登入。外部管理的 daemon 不提供此啟動操作。

此修正尚未打包成新的 macOS App，亦未在 M5 驗證。既有 App 若出現 daemon 登入過期提示，可先使用其中的「重新登入」恢復憑證；這也是 App 內部流程，不依賴 PATH。

### Desktop token 刪除保護（後續候選，尚未安裝）

App 的 API token 頁會辨識目前 Desktop profile 實際使用的 token，不只比對名稱。刪除此 token 時提供「取消」或「重新生成並重新連線」：新 token 先產生並寫入，確認 daemon 使用新設定重新啟動且回報 running 後，才撤銷舊 token。執行中的任務或無法確認 daemon 狀態時不執行輪替；身分辨識失敗時停用刪除。

產生失敗保留原憑證；重啟失敗保留舊 token 的有效性並回報新設定已保存，使用守護程序設定重試。新連線成功但撤銷失敗時明確提示手動刪除舊項目，不宣稱全部完成。取消不會刪除或產生 token。其他機器的 token 不會用本機 daemon 取代。

### 繁中版 Server 預設值（HYCLV-64 後續候選）

一般 `multica setup` 預設 server_url/app_url 均為 `https://10.1.24.90:45671`，依 flags → environment → 既有 profile → 繁中預設選用 URL。已指定的 profile URL 不會因重新 setup 自動變回官方 Cloud。明確執行 `setup cloud` 仍代表選擇官方服務；`setup self-host` 保留上游明確選用 localhost／自訂服務的用途。

未配置 Server 的 `login --token` 與 Desktop 缺少正式 runtime 設定時，也預設連 s90；Desktop 開發模式維持 localhost。既有 explicit runtime config／環境變數仍優先。

已被舊版 setup 寫成官方 URL 的 v6 profile，請先用以下指令改回；設定 URL 不會啟動或重啟 daemon：

```bash
/mnt/data-home/hungyu/multica-zh-tw/v6/bin/multica --profile v6 config set server_url https://10.1.24.90:45671
/mnt/data-home/hungyu/multica-zh-tw/v6/bin/multica --profile v6 config set app_url https://10.1.24.90:45671
/mnt/data-home/hungyu/multica-zh-tw/v6/bin/multica --profile v6 login --token
```

舊版 binary 更新前避免再次執行不帶子命令的 setup。新版 setup 仍是互動設定／登入及 daemon 啟動流程，不只是修改 URL；執行前留意現有任務。此修正不自動遷移舊設定或替換已安裝 binary。


### 新使用者簡化安裝（候選，尚未發布下載入口）

發行包將內含已確認的 s90 **公開 CA 憑證**，與 CLI 一起受 installer 固定 SHA256 保護；發行建置須明確提供該憑證路徑，不包含私鑰。安裝腳本必須由可信 HTTPS 發布來源下載，不能從未驗證的 s90 HTTPS 用 -k 自行取得信任根。

使用者下載該版本 install.sh 後執行 `bash ./install.sh --login`：自動辨識平台、下載並驗證成品、組合系統公用 CA + s90 CA、建立自己的 launcher，提示輸入 API token，登入成功後啟動 daemon。不輸入 URL、profile 或 SSL_CERT_FILE。沒有 --login 時只安裝並顯示後續指令，不啟動服務。

launcher 為 `~/.local/bin/multica`，設定對外 Server/App URL `https://10.1.24.90:45671`，不帶 `--profile`，設定使用 `~/.multica/config.json`。同機多人使用預設 profile 的 health port 隔離尚待處理與驗證，不能據此宣稱已支援同機多人同時啟動。CA 僅由 launcher 帶入，組合公用 roots 避免 provider 工具失去原有公用 CA。

這是新使用者安裝路徑，不會搬移或覆蓋既有 hungyu 的 v6/s90 binary。現有安裝會安全退出，升級另按部署流程。CLI 指令：`~/.local/bin/multica auth status`；停止：`~/.local/bin/multica daemon stop`。尚未配置 systemd／登入後開機自動啟動。

s90 本機 daemon 的 `http://127.0.0.1:45673` 是直接 backend 入口；其他機器使用 `https://10.1.24.90:45671` 經 Caddy TLS 入口，兩者連同一 Server。不能把 s90 localhost URL 複製到同事機器。


### profile 的用途（最新安裝決策）

新安裝使用 `~/.local/bin/multica` launcher，內建網址與 CA 處理，但不指定 profile 名稱。這是命令列入口，不是 macOS GUI App。設定屬於目前 OS 帳號，預設位於 `~/.multica/config.json`。

具名 CLI profile 是同一 OS 帳號下不同連線／設定組合：可保存不同 Server、Multica 登入 token、預設工作區及 daemon 參數。它不切換 Linux 使用者，也不等於 Agent 人格；工作區是 Server 上的協作空間，同一登入可存取多個工作區。多個具名 profile 共用 state root 的 daemon ID，不能當成 OS 安全邊界。只有需要同帳號管理不同 Server／登入設定時才手動使用 `--profile`。


### 每人、多台機器的安裝模型

每位使用者有一個 Multica Server 帳號，可在 v6、s234、s90 各自的 OS 帳號下運行一個 daemon。每台機器獨立安裝、保留自己的 daemon 身分與 API token；不複製整份 ~/.multica 到其他機器。同一登入可參與多個 Server 工作區，工作區不等於本機工作目錄。

新安裝器明確設定 workspaces_root=$HOME/.multica/workspaces；config/token 為 ~/.multica/config.json，task checkout 與執行環境在 workspaces/ 子目錄。程式原本的 ~/multica_workspaces fallback 不在本次全域修改，既有安裝不搬移。~/.multica 可能含原始碼、產物及憑證，不是可以任意整包清除的 cache，也不應跨機器共用同一個可寫 state root。
