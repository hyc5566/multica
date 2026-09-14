# agy 的供應商配額窗口

HYCLV-80 候選功能，基底 `zh-tw`：`b7934d3c5482b4d73d4c40f4888df365debf7c0b`。
本文件是與程式綁定的主要規格／重建指引；候選驗證不代表正式部署。

## 已查證的來源（2026-09-13，Asia/Taipei）

m5 `chy1010-linker-m5.local` 上 agy-hud plugin 0.3.3，Git HEAD
`54bced590656e7b671b87469c44273cbb1d356d9`，帶有未提交客製修改。
來源位於 `~/.gemini/config/plugins/agy-hud/runtime`；執行副本
`~/.gemini/antigravity-cli/agy-hud-runtime/runtime` 的相關檔案雜湊一致。

- `quota/cloud.js:177` 使用 `fetchAvailableModels`。
- `quota/models.js:95` 依距離重置時間是否小於 12 小時推測窗口；週額度即將重置時會誤判。
- `renderer/quota-render.js:35` 的四格解析來自未提交修改；`:127` 缺窗口補剩餘 100%，不能作為真實用量。
- 不採用這些推測、補值或 HUD 快取；模型資料相等也不是共用池證據。

另外從 `~/.local/bin/agy` 的內建 protobuf descriptor 確認
`RetrieveUserQuotaSummaryRequest/Response`、HTTP POST 與 body `*`。
該 binary SHA256：`cabadc15a61944372bede1fdff186701c17467dd9d718e97dc79283055d3c101`。
2026-09-13，由 s90 經既有 SSH 通道在 m5 記憶體內使用當地登入憑證，
POST 下列 endpoint、body `{}`，得到 HTTP 200 與以下分組。未執行模型、未寫入 m5，
未保存原始回應或憑證。此來源是 agy 使用的 Google 後端，並非保證相容的公開 API。

`https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary`

| 回應 group | bucketId | window | UI 語意 |
| --- | --- | --- | --- |
| Gemini Models | gemini-5h | 5h | Google 模型共用 5 小時額度 |
| Gemini Models | gemini-weekly | weekly | Google 模型共用每週額度 |
| Claude and GPT models | 3p-5h | 5h | Claude／GPT 共用 5 小時額度 |
| Claude and GPT models | 3p-weekly | weekly | Claude／GPT 共用每週額度 |

`remainingFraction` 是剩餘比例，已用百分比為 `100 - remainingFraction * 100`。
`resetTime` 為 RFC3339；窗口只依明確的 `window`，不根據倒數時間判斷。
群組有自己的 buckets；也支援 schema 定義的頂層 buckets。各 bucket 保留獨立 ID，
不合計、平均、取第一個模型或依相同比例去重。重複 ID／無法辨識結構會回報錯誤。

## 顯示與缺值規則

- Agents 頁面顯示兩個共用池、各自 5 小時與每週窗口；目前模型所屬池的兩窗一起標示。
- Gemini model ID 前綴對應 Google；Claude／GPT model ID 前綴對應第三方池。
  未知模型不任意標成第一個池。未來其他供應商／獨立 bucket 原樣保留，不硬併進第三方池。
- 缺失、非數字、布林值、非有限值、超過 0–1 的 fraction、disabled 或 remainingAmount
  不轉成百分比。amount 是不同單位，不能當 fraction；UI 顯示「來源未提供可用的百分比」。
- 缺任一已知窗口或有不可用數值／未知窗口週期，快照標 partial。UI 缺窗只加無數值的占位，
  不生成假重置時間。來源完全沒有 bucket 或結構錯誤時回報 error，保留上次成功快照。
- 過重置時間隱藏百分比；超過既有快照時效或重新整理失敗時沿用既有歷史資料提示。
- 舊 daemon 回傳的模型額度仍可顯示，但不將它改稱 5 小時或週用量。
- Task quota consumers 選取所用模型對應的兩窗並標記 provider/shared，仍不是單次任務消耗。
  歷史 model-ID／tiered 快照保留原有精確比對；未知模型不暴露全部窗口。

## 重建位置與隔離

1. `server/pkg/agent/provider_usage_probe.py`：更換 Antigravity 的單一直接查詢 endpoint 與 normalizer。
   保留既有憑證、TLS、限流與錯誤處理；不引入新的憑證、帳號快取或設定欄位。
2. daemon 仍透過 `observeTaskQuota`／provider_usage dispatch 上報原有正規化格式。
   Server snapshot key 仍為 daemon + provider 或獨立 profile；UI query key 仍包含 runtime ID。
3. `packages/views/agents/components/antigravity-usage.ts` 與 `agent-usage-summary.tsx`：共用 Web／Desktop
   呈現，沿用正規化 duration、百分比、resetTime 與現有橫向捲動。en 與 zh-Hans（繁中版）新增文案。
4. `server/internal/handler/task_quota_checkpoint.go`：接上新池 ID，保留歷史快照處理。
5. 不需要 migration、額外 HTTP route、新 dependency 或 m5 plugin 修改。

既有 `MULTICA_PROVIDER_USAGE_PYTHON` 可指定 daemon 使用的直譯器；候選測試在 s90 使用
`$HOME/miniconda3/envs/hungyu/bin/python`。其餘限流/TLS 設定見
[直接配額 probe](../server/pkg/agent/provider_usage_probe.md)。

## 驗證與發布

- Python：`"$HOME/miniconda3/envs/hungyu/bin/python" -m unittest server/pkg/agent/provider_usage_probe_test.py`
- UI：`pnpm --filter @multica/views test agents/components/agent-usage-summary.test.tsx agents/components/antigravity-usage.test.ts`
- 既有隔離／重新整理：`pnpm --filter @multica/core test runtimes/provider-usage.test.ts`
- 型別：`pnpm --filter @multica/views typecheck`
- Go probe：在 server 執行 `GOMAXPROCS=2 go test -p 2 ./pkg/agent -run '^TestParseProviderUsageProbe' -count=1`。
- Handler 純測試需要先處理 `handler_test.go` 的 TestMain：`-run` 仍會執行 DB 初始化。
  本候選用 Go overlay 僅略過 TestMain DB setup，執行 TaskQuota 與 in-memory provider roundtrip，
  不連正式或預設 DB；未宣稱通過 DB 整合測試。完整套件需另備隔離資料庫。
- CLI 建置：在 server 執行 `GOMAXPROCS=2 go build -p 2 -o <候選路徑> ./cmd/multica`。
- 畫面證據是實際元件搭配合成測試資料與共用 design tokens 的瀏覽器預覽，非正式頁面截圖。

發布需另核准，並更新實際執行 agy 的 daemon（含嵌入的 probe）、Server 的 task quota consumer，
以及使用 Agents UI 的 Web／Desktop。僅改 UI 無法使舊 daemon 取得真實四窗口。
不同帳號方案或未來 schema 仍須依實際回應確認；失敗不可回退到 HUD 的推算百分比。
正式版本切換／恢復依 [Server 交接程序](server-handoff.zh-tw.md)，本候選不執行部署。

## 2026-09-13：有效登入憑證選取

讀取 Keychain 與 agy 專用檔案時，忽略已過期或無法解析有效期的憑證；有效 Keychain 優先，其次為 Terminal 更新的 agy-hud-token.json，最後為 antigravity-oauth-token。HUD 列表跳過過期項目。舊格式未記錄有效期時維持相容，仍以官方 API 的驗證結果為準。
不再拿通用 Gemini oauth_creds.json 作為 agy 憑證，避免查到其他產品／帳號的額度。不自行續期、改寫憑證或執行模型請求；所有來源失效時仍顯示 auth_required。
回歸測試涵蓋過期檔遮蔽有效HUD、過期Keychain、時區／過期邊界／格式錯誤，以及不使用通用Gemini登入。

憑證 expiry 的 Go RFC3339Nano 小數秒先正規化為六位，再解析，以相容 m2 Python 3.10；測試包含1、5、9位小數。


## 2026-09-15：HYCLV-93 到期後續期與混合版本修復

此次現場發現 m2／m5 的 App .14、daemon .12 均已安裝且運作；Multica 登入有效，
但 Google access token 到期後，額度排程持續得到 auth_required，Server 保留過期快照。
9/13 的選取修正只跳過過期來源，沒有後續續期流程；本節取代前節「不自行續期」對 daemon
排程的限制，Python 直接 probe 仍不寫入憑證。

- Python 只有在所有 access token 都無法使用，且原生 agy Keychain／專用檔案存在已到期、
  可解析 expiry 與 refresh_token 時，回傳不含秘密的續期提示；不使用通用 Gemini 登入。
- 手動／排程的內建 agy runtime 使用已註冊執行檔，固定執行一次未快取的 `models`，
  最多10秒；由原生 CLI 管理 Google 更新。它不執行模型推論，stdout／stderr 不上報。
  完成後重新查官方 quota，只有此查詢成功才更新成功快照。無憑證、無有效 refresh token、
  撤銷授權或更新失敗仍明示失敗，不能把原生命令退出0當成額度成功。
- 手動／排程總期限45秒；任務 checkpoint維持3秒，即使等待共用刷新也不能拖長任務。
  不在任務前後自動續期，不變更其他provider或custom profile的憑證行為。
- 最新zh-tw曾遺漏.14兩個既有判斷：只有真的共用池資料才補缺窗口；精確獨立model bucket
  優先於推測的共用池。已恢復並加入回歸；不是重新設計畫面。
- 一排額度卡片維持當前5h、weekly、其餘；offline仍顯示保留快照並停用手動按鈕，
  授權失敗改說明額度憑證／續期，避免宣稱整個agent未登入。

沒有新DB欄位、schema migration、provider端點或相依套件。`credential_refresh_needed`
只供本地daemon判斷，既有Server忽略未知欄位即可接受結果。相容於既有.6 Server；
仍需對發布成品完成真實兩機驗證，mock通過不代表Google續期／App重開已驗收。

重建採兩條候選：最新zh-tw維護分支與從已部署.14基底c0f305ab4回補的發布候選，
後者只帶本議題修復，避免把未部署的上游升級一起帶入兩台Mac。m5原autoStart=false
另需核准改為true（autoStop=false）；此為原生偏好，並非程式版本能自行代替。

限制：不自動切換不同帳號，不因401就遍歷可能屬於其他帳號的token。Custom profile
續期未啟用；現有quota helper仍以daemon OS user取得資料，不能宣稱支援profile專屬帳號。
Google撤銷refresh grant、Keychain不可讀且無專用檔案，仍可能需要使用者授權；兩機到期後
更新、App重開、重連、CA持久性與跨至少一次真實expiry邊界的驗收均應分項記錄。
