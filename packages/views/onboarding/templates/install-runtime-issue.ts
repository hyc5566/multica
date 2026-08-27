/**
 * Skip path: "Connect a runtime to start with Mika".
 *
 * Written to a new issue (assigned to the user themselves) by the welcome
 * hook when the user took the Skip exit on Step 3. Content is the
 * install-runtime tutorial; each supported locale can recommend the
 * quickest runtime path that best fits that audience.
 *
 * Title is stable — kept identical to the v2 server-side
 * `NoRuntimeIssueTitle` so any existing dedupe code elsewhere keeps
 * matching by title.
 */

/**
 * Localized so users see the title in their current supported locale on the
 * board. The Runtimes page owns the follow-up Mika bootstrap once a runtime
 * appears, so this guide does not ask the member to copy an agent prompt.
 *
 * Note: server's deprecation shim (`onboarding_shim.go:noRuntimeIssueTitle`)
 * still uses the bare English string for its title-based dedupe — that
 * codepath only runs for pre-v3 desktop builds and never overlaps with
 * the v3 frontend population, so the two title-spaces drifting is fine.
 */
export const INSTALL_RUNTIME_ISSUE_TITLE = {
  en: "Connect a runtime to start with Mika",
  zh: "連線執行環境，和 Mika 開始",
  ko: "runtime을 연결하고 Mika와 시작하기",
  ja: "runtime を接続して Mika と始める",
} as const;

const en = `Welcome to Multica.

Agents need a runtime before they can execute work. You can still use Multica as a lightweight project-management workspace while you install one.

## Try Multica first

Before the runtime is ready, you can:

1. Create a project for your current work.
2. Create a few issues and move them across backlog, todo, in_progress, and done.
3. Add priorities, labels, comments, and subscriptions.
4. Use Inbox to track assignments and mentions.

That gives you the project-management layer first. Once a runtime is connected, agents can start working from the same issues.

## Install your first agent runtime

Full guide: https://multica.ai/docs/install-agent-runtime

For English users, the fastest first path is Codex:

1. Make sure Node.js is installed.
2. Install Codex:
   npm i -g @openai/codex
3. Sign in:
   codex
4. Confirm your terminal can find it:
   which codex
   codex --version
5. Wait for Multica to pick it up. A running daemon re-checks for newly
   installed CLIs every couple of minutes, so no restart is normally needed.
   To apply it immediately:
   multica daemon restart
   In the desktop app, open any local runtime and click Restart. Quitting and
   reopening the app is NOT enough — the daemon keeps running in the background.
6. Return to Runtimes and refresh. You should see a Codex runtime online.
7. Open Runtimes. The page will offer **Start with Mika**; use it to create Mika and open the guided first chat.

Codex reference: https://developers.openai.com/codex/cli

Mika will turn one real goal into an issue, start it with the right agent, and suggest reusable specialists when your workflow needs them.`;

const zh = `歡迎來到 Multica。

智能體需要先連上執行環境才能執行工作。執行環境還沒準備好時,你也可以先把 Multica 當作輕量專案管理工具體驗起來。

## 先體驗專案管理功能

執行環境安裝前,你可以先做這些事:

1. 為當前工作建立一個專案。
2. 新建幾個任務,並在 backlog、todo、in_progress、done 之間流轉。
3. 給任務加優先順序、標籤、評論和訂閱。
4. 用收件匣追蹤分配給你的事項和 @mention。

這樣你先熟悉專案管理層。連上執行環境後,智能體會直接在這些任務上開始工作。

## 安裝第一個 Agent 執行環境

完整文件:https://multica.ai/docs/install-agent-runtime

中文使用者建議先裝 Kimi CLI:

1. 在 macOS / Linux 終端安裝 Kimi CLI:
   curl -LsSf https://code.kimi.com/install.sh | bash
   Windows PowerShell:
   Invoke-RestMethod https://code.kimi.com/install.ps1 | Invoke-Expression
2. 確認終端能找到 Kimi:
   kimi --version
3. 在你想讓 Kimi 工作的專案目錄裡啟動一次:
   kimi
4. 首次啟動後輸入 /login,按提示完成 Kimi Code 或 API key 配置。
5. 等 Multica 識別到它。執行中的守護程序每隔幾分鐘會重新檢查一次新裝的 CLI,通常不需要重啟。
   想立刻生效:
   multica daemon restart
   桌面端請開啟任意一個本機 runtime 並點 Restart。退出再開啟 app 是不夠的 —— 守護程序會繼續在後臺執行。
6. 回到 Runtimes 頁面重新整理。你應該能看到一個線上的 Kimi 執行環境。
7. 開啟"執行環境"頁面。頁面會顯示 **和 Mika 開始**；點選後會建立 Mika，並進入引導式的首次對話。

Kimi CLI 官方文件:https://moonshotai.github.io/kimi-cli/zh/guides/getting-started.html

Mika 會把一個真實目標轉化為任務，交給合適的智能體啟動執行，並在工作流需要時建議新增可複用的 specialist。`;

const ko = `Multica에 오신 것을 환영합니다.

agent가 작업을 실행하려면 먼저 runtime이 필요합니다. runtime을 설치하는 동안에도 Multica를 가벼운 프로젝트 관리 워크스페이스로 먼저 사용할 수 있습니다.

## 먼저 Multica를 사용해 보기

runtime이 준비되기 전에는 다음을 해볼 수 있습니다:

1. 현재 작업을 위한 project를 만듭니다.
2. 태스크 몇 개를 만들고 backlog, todo, in_progress, done 사이에서 이동해 봅니다.
3. priority, label, comment, subscription을 추가합니다.
4. Inbox에서 나에게 배정된 작업과 mention을 확인합니다.

이렇게 프로젝트 관리 계층을 먼저 익힐 수 있습니다. runtime이 연결되면 agent가 같은 태스크에서 바로 작업을 시작합니다.

## 첫 agent runtime 설치하기

전체 가이드: https://multica.ai/docs/install-agent-runtime

한국어 사용자는 Codex로 시작하는 것이 가장 빠릅니다:

1. Node.js가 설치되어 있는지 확인합니다.
2. Codex를 설치합니다:
   npm i -g @openai/codex
3. 로그인합니다:
   codex
4. 터미널에서 찾을 수 있는지 확인합니다:
   which codex
   codex --version
5. Multica가 인식할 때까지 기다립니다. 실행 중인 daemon은 몇 분마다 새로 설치된 CLI를
   다시 확인하므로 보통 재시작이 필요하지 않습니다.
   바로 적용하려면:
   multica daemon restart
   데스크톱 앱에서는 아무 로컬 runtime을 열고 Restart를 누르세요. 앱을 종료하고 다시 여는
   것만으로는 충분하지 않습니다 — daemon은 백그라운드에서 계속 실행됩니다.
6. Runtimes로 돌아가 새로고침합니다. Codex runtime이 online으로 보여야 합니다.
7. Runtimes를 엽니다. **Mika와 시작**을 눌러 Mika를 만들고 안내되는 첫 채팅을 시작합니다.

Codex 참고 문서: https://developers.openai.com/codex/cli

Mika가 실제 목표 하나를 태스크로 만들고 적합한 에이전트와 실행을 시작하며, 워크플로에 필요할 때 재사용 가능한 specialist를 제안합니다.`;

const ja = `Multica へようこそ。

agent が作業を実行するには、まず runtime が必要です。runtime をインストールしている間も、Multica を軽量なプロジェクト管理ワークスペースとして先に使うことができます。

## まず Multica を使ってみる

runtime が準備できる前に、次のことを試せます:

1. いまの仕事のための project を作る。
2. タスクをいくつか作り、backlog、todo、in_progress、done の間で動かしてみる。
3. priority、label、comment、subscription を追加する。
4. Inbox で自分への割り当てや mention を確認する。

これでまずプロジェクト管理のレイヤーに慣れることができます。runtime を接続すると、agent が同じタスクから作業を始められます。

## 最初の agent runtime をインストールする

詳しいガイド: https://multica.ai/docs/install-agent-runtime

日本語ユーザーには、Codex で始めるのが最も速い経路です:

1. Node.js がインストールされていることを確認します。
2. Codex をインストールします:
   npm i -g @openai/codex
3. サインインします:
   codex
4. ターミナルから見つけられるか確認します:
   which codex
   codex --version
5. Multica が認識するまで待ちます。動作中の daemon は数分ごとに新しくインストールされた
   CLI を再チェックするため、通常は再起動は不要です。
   すぐに反映したい場合:
   multica daemon restart
   デスクトップアプリではローカル runtime を開いて Restart を押してください。アプリを終了して
   開き直すだけでは不十分です — daemon はバックグラウンドで動き続けます。
6. Runtimes に戻って再読み込みします。Codex runtime が online と表示されるはずです。
7. Runtimes を開き、**Mika と始める**を選びます。Mika が作成され、案内付きの最初のチャットが開きます。

Codex のリファレンス: https://developers.openai.com/codex/cli

Mika は実際の目標を 1 つのタスクにし、適切なエージェントで実行を開始し、ワークフローに必要なときは再利用可能な specialist を提案します。`;

export const INSTALL_RUNTIME_ISSUE_BODY = { en, zh, ko, ja } as const;
