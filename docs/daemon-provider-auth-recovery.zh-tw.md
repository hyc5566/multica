# Daemon provider CLI 認證失效處理

Daemon 依 `taskfailure.Classify` 將 provider 回報的 401、403、未登入、登入逾期、refresh token 失效與無效 API key 判定為認證／存取失效。其他網路、配額及服務錯誤不啟動登入流程。

Linux GNOME 桌面支援內建 Codex 與 Claude CLI。只有 daemon 繼承到 `DISPLAY` 或 `WAYLAND_DISPLAY`，且可找到 `gnome-terminal` 時，才會開啟終端機執行 `codex login` 或 `claude auth login`。登入最長等待 10 分鐘；登入命令成功且 agent 尚未執行任何工具時，原工作以相同 session/options 重試一次。若已有工具執行，仍可登入，但不自動重播，以免重複副作用；使用者可手動重試並沿用既有 session。登入視窗關閉、命令失敗、使用者取消或逾時都會停止自動重試並回報手動指令。登入輸出只顯示在該終端機，不由 daemon 擷取或記錄。

Headless、SSH、沒有 GNOME Terminal、Windows/macOS、custom runtime 及其他 provider 不會嘗試開 UI。錯誤狀態會提供對應內建 CLI 的登入指令（Codex：`codex login`；Claude：`claude auth login`）；custom/未知 provider 會要求使用該 provider 官方登入方式。使用者需在 daemon 相同 OS 帳號下完成登入，再從 Multica 重試工作。

CLI 登入流程及命令依官方文件：

- [OpenAI Codex CLI `codex login`](https://developers.openai.com/codex/cli/reference)
- [Claude Code CLI `claude auth login`](https://code.claude.com/docs/en/cli-usage)
