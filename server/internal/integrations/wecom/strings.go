package wecom

// strings.go — every string a WeCom user reads, in one place, selected per
// READER.
//
// The copy used to sit inline wherever it was sent: the offline and archived
// notices in replier.go, the binding prompt built by hand a few lines below
// them, the inbox card's type labels in inbox_message.go. All of it Chinese,
// which is right for our own tenant and wrong the moment anyone else installs
// the bot — and impossible to find, because "what does this bot say" was
// spread across three files.
//
// Which pack a given message uses is decided by the DESTINATION, not by the
// installation (language.go): a 1:1 gets that person's Multica profile
// language, and a room — where there is no shared profile and no member list —
// gets the deployment's own language.
//
// Slack's adapter hardcodes English and Lark's hardcodes Chinese, so there is
// no house i18n mechanism to join. This is deliberately not one either: a
// struct of strings per locale. No catalogue files, no message ids, no plural
// rules. If wecom ever needs a third language with real formatting rules,
// that is the moment to reach for a framework — not now.
//
// The zh-Hans compatibility key serves Taiwan Traditional Chinese copy in
// this fork. Keep the locale key stable for stored profile preferences.

import (
	"strings"
	"sync/atomic"
)

// Locale names the language an installation's users are answered in.
type Locale string

const (
	LocaleZhHans Locale = "zh-Hans"
	LocaleEn     Locale = "en"

	// DefaultLocale is the compile-time fallback: Chinese, because WeCom
	// is a Chinese platform. It is what a deployment that says nothing gets.
	// Read deploymentLocale() rather than this — a deployment can say
	// otherwise, and a room's language is a property of the deployment, not of
	// whichever person happened to speak.
	DefaultLocale = LocaleZhHans
)

// deploymentLocaleValue is the language this server answers in when the reader
// is a room, or a person whose profile says nothing. Set once at boot from
// MULTICA_WECOM_DEFAULT_LOCALE (cmd/server/router.go) and read on every
// message, so it is an atomic rather than a plain var: -race would otherwise
// flag the boot write against the first inbound frame.
//
// It exists because the alternative answers are all worse. A hardcoded
// constant makes an English-speaking tenant's rooms Chinese with no way out
// but a rebuild. An installation-level column was tried and removed: nothing
// could write it. Borrowing the installer's personal profile language repeats,
// with a different person, the exact bug that motivated this — one member's
// setting deciding what a whole room reads. A deployment-level knob is the
// smallest thing that is actually about the deployment.
var deploymentLocaleValue atomic.Value

// SetDeploymentLocale fixes the deployment's language from a raw config string
// and returns what it resolved to, so the caller can log it. Anything
// unrecognised — including empty — leaves the current value in place: a typo in
// an env var must not silently switch a tenant's language.
//
// Deliberately NOT resolveLocale. That one reads a user's profile field, which
// the API has already validated to en / zh-Hans / ko / ja, so it can treat
// "anything that isn't Chinese" as a deliberate choice of the English pack. An
// env var has been validated by nobody: under that rule
// MULTICA_WECOM_DEFAULT_LOCALE=zh_Hant, or a stray quote, would quietly put a
// Chinese tenant's rooms into English. So this one matches exactly, and an
// operator who mistypes gets the old language and a log line, not a surprise.
func SetDeploymentLocale(raw string) Locale {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "zh-hans", "zh":
		deploymentLocaleValue.Store(LocaleZhHans)
		return LocaleZhHans
	case "en":
		deploymentLocaleValue.Store(LocaleEn)
		return LocaleEn
	default:
		return deploymentLocale()
	}
}

// deploymentLocale is the configured language, or DefaultLocale before
// anything has configured one — which is also what every test sees.
func deploymentLocale() Locale {
	if v, ok := deploymentLocaleValue.Load().(Locale); ok {
		return v
	}
	return DefaultLocale
}

// resolveLocale maps a user's profile language onto a supported Locale. The
// profile validates to en / zh-Hans / ko / ja (handler/auth.go), and there are
// two packs: Chinese for zh*, English for everyone who chose anything else —
// a ko or ja user deliberately picked "not Chinese", and English is the
// lingua-franca pack we have for them. Only an EMPTY value falls back to
// deploymentLocale: absence is not a choice.
func resolveLocale(s string) Locale {
	switch v := strings.ToLower(strings.TrimSpace(s)); {
	case v == "":
		return deploymentLocale()
	case strings.HasPrefix(v, "zh"):
		return LocaleZhHans
	default:
		return LocaleEn
	}
}

// copyPack is the full set of user-visible strings for one locale. Everything
// the adapter can say is a field here; nothing is built by concatenating
// fragments elsewhere.
type copyPack struct {
	// AgentOffline / AgentArchived answer an engine outcome.
	AgentOffline  string
	AgentArchived string

	// FreshPending confirms /clear: the next chat message stays in the
	// conversation it is already in and runs without the context before it.
	// ChatStarted confirms /new, which is the other half of that split — a
	// new conversation the next message enters. Two commands, two answers:
	// telling a /clear user their conversation was replaced sends them
	// looking for a thread that never moved. IssueUsage answers a bare
	// /issue with the shape it wanted. All three are stated here rather than as
	// package constants — the lint asserts it, and a Chinese literal in
	// replier.go is a line no other locale can read.
	FreshPending string
	ChatStarted  string
	IssueUsage   string

	// InvokeDenied answers a member who may not run this agent (a private
	// agent belongs to its owner alone). It always goes to that member alone —
	// in place in a 1:1, in their own 1:1 for a group trigger (replier.go) — so
	// their profile picks the language even when the trigger was a room.
	InvokeDenied string

	// BindingPromptPrefix / BindingPromptSuffix wrap the bind URL.
	// BindingPending replaces the whole thing when the mint was throttled and
	// there is no URL to print. Both go to the sender alone, never to a room
	// (replier.go) — the URL carries a bearer token.
	BindingPromptPrefix string
	BindingPromptSuffix string
	BindingPending      string

	// BindingSentPrivately is what the ROOM sees when an unbound member
	// triggers the bot in a group: the prompt itself went to that member's
	// 1:1 chat, and without this line the group would read the bot as broken.
	// It names no one and carries no token, so it is safe in front of an
	// audience of unknown size.
	BindingSentPrivately string

	// IssueCreatedPrefix leads the /issue confirmation; IssueTitleSeparator
	// joins the identifier to the title when there is one.
	IssueCreatedPrefix  string
	IssueTitleSeparator string

	// IssueDuplicatePrefix leads the answer for an /issue the engine refused
	// because an active issue already matched. The identifier and title that
	// follow belong to THAT issue, which is why the copy has to say so rather
	// than reading like a confirmation: this case used to be answered with
	// the created copy, so the person was shown a number somebody else opened,
	// under a title they never wrote, for a report that was never filed.
	IssueDuplicatePrefix string

	// The ways a streaming reply ends in something other than an answer. Each
	// one closes the loading bubble the question opened, so each one has to
	// carry visible text — WeCom discards a closing frame it considers empty
	// and the bubble spins on forever (see hasVisibleChar in ws_frame.go).
	//
	// StreamNoReply — the agent finished with nothing to say.
	// StreamNoReplyWithFiles — the agent finished with no words but produced
	//   files, which arrive as separate messages right after this one.
	//   Distinct from StreamNoReply because that copy says nothing is coming,
	//   and then something arrives: a bubble that contradicts the next message
	//   reads as a bug even though both halves are working.
	// StreamNotStarted — no run was triggered at all (agent offline or
	//   archived, or the enqueue failed); the replier's own notice follows as
	//   a separate message with the detail.
	// StreamFailed — the run failed, and the platform published no reason of
	//   its own. A failure that DID carry one says that instead (failureText
	//   in typing_indicator.go).
	// StreamCancelled — the user stopped the run, so no answer is coming.
	//   Separate copy from StreamFailed on purpose: inviting a retry of
	//   something somebody just stopped on purpose reads as the bot not having
	//   noticed.
	StreamNoReply          string
	StreamNoReplyWithFiles string
	StreamNotStarted       string
	StreamFailed           string
	StreamCancelled        string

	// InboxDetailLink is the anchor text on the inbox card's deep link;
	// InboxTypeLabels names each notification kind, with InboxTypeFallback
	// covering a kind this adapter has not been taught yet.
	InboxDetailLink   string
	InboxTypeLabels   map[string]string
	InboxTypeFallback string
	// MediaSendFailed / MediaSendUnknown / MediaLookupFailed go the other way:
	// the agent produced a file and it did not plainly reach the chat. The
	// answer is already on screen and may well point at the file, so silence
	// would leave the user waiting for something that is not coming. One line
	// covers the whole turn however many files were affected — the reason (too
	// big, storage down, WeCom refused it) is a log line, not something the
	// reader can act on.
	//
	// Three of them because there are three things that can be true, and
	// telling them apart is the point (see deliveryState in outbound_media.go).
	// MediaSendFailed is the definite one: nothing arrived and nothing can
	// have. MediaSendUnknown is for a send whose verdict never came back — its
	// wording has to survive both endings, so it must not say "failed" to
	// someone looking at the file, and it says why nothing is resent, since a
	// duplicate cannot be taken back. MediaLookupFailed is earlier still: we
	// could not read what was attached to this reply, so we do not know whether
	// there was a file at all.
	MediaSendFailed   string
	MediaSendUnknown  string
	MediaLookupFailed string
}

// label returns the display name for an inbox notification type.
func (c copyPack) label(t string) string {
	if l, ok := c.InboxTypeLabels[t]; ok {
		return l
	}
	return c.InboxTypeFallback
}

// issueCreated renders the /issue confirmation.
//
// The title is whatever the reporter typed, and this goes out as a markdown
// body — so it goes through breakMemberLinks here, at the render boundary,
// exactly as buildInboxMarkdown treats a title before putting it in a card.
// Without it, "/issue 安全升级：请点击 [重置密码](https://evil.example) 完成验证"
// comes back from the Multica bot, in the group, as a working link inside a
// confirmation everyone has reason to trust. Our own copy fragments stay raw;
// only the member-authored field is broken.
func (c copyPack) issueCreated(identifier, title string) string {
	title = breakMemberLinks(strings.TrimSpace(title))
	if title == "" {
		return c.IssueCreatedPrefix + identifier
	}
	return c.IssueCreatedPrefix + identifier + c.IssueTitleSeparator + title
}

// issueDuplicate renders the answer for an /issue the engine refused because
// an active issue already matched. The identifier and title belong to THAT
// issue, not to the message being answered.
//
// The title here belongs to somebody ELSE's issue, which makes breaking its
// links more important rather than less: the reporter is being shown text they
// did not write.
func (c copyPack) issueDuplicate(identifier, title string) string {
	out := c.IssueDuplicatePrefix + identifier
	if t := breakMemberLinks(strings.TrimSpace(title)); t != "" {
		out += c.IssueTitleSeparator + t
	}
	return out
}

// copyFor returns the pack for a locale, falling back to the deployment's.
func copyFor(l Locale) copyPack {
	if pack, ok := copyPacks[l]; ok {
		return pack
	}
	return copyPacks[deploymentLocale()]
}

var copyPacks = map[Locale]copyPack{
	LocaleZhHans: {
		AgentOffline:         "⚠️ Agent 目前離線，你的訊息已收到，等它上線後會處理。",
		AgentArchived:        "⚠️ 該 Agent 已歸檔，無法回覆。請聯絡工作區管理員。",
		FreshPending:         "✅ 已準備從空白上下文執行。你的下一則聊天訊息仍會進入目前對話，但不會帶上之前的上下文。",
		ChatStarted:          "✅ 已建立 Multica 對話。你的下一則訊息會進入該對話。",
		IssueUsage:           "請填寫任務標題，格式如下：\n\n`/issue <標題>`\n`[描述]`（可選）",
		InvokeDenied:         "⚠️ 你沒有權限執行該 Agent。如需使用，請聯絡它的擁有者。",
		BindingPromptPrefix:  "👋 請先綁定你的 Multica 帳號，才能與我對話：\n",
		BindingPromptSuffix:  "\n（連結 15 分鐘內有效）",
		BindingPending:       "👋 綁定連結剛才已經發給你了，就在上方，請直接點選完成綁定。",
		BindingSentPrivately: "👋 已把綁定連結傳送到你的私人對話，請在與我的私人對話裡點選完成綁定。",

		IssueCreatedPrefix:   "✅ 已建立 ",
		IssueTitleSeparator:  " — ",
		IssueDuplicatePrefix: "⚠️ 未建立 —— 已存在進行中的 ",

		StreamNoReply:          "（這輪沒有需要回覆的內容）",
		StreamNoReplyWithFiles: "（這輪沒有文字回覆，附件在下面）",
		StreamNotStarted:       "已收到，但這則暫時沒能開始處理。",
		StreamFailed:           "⚠️ 這次執行失敗，請稍後再試一次。",
		MediaSendFailed:        "⚠️ 有檔案無法傳送，我這邊保留著，需要的話我再試一次。",
		MediaSendUnknown:       "⚠️ 有檔案我沒收到企業微信的送達確認，可能已經送達、也可能沒有。我不會自動重送，避免重複傳送；你那邊沒看到的話說一聲，我再傳送一次。",
		MediaLookupFailed:      "⚠️ 我這邊沒查到這則回覆是否有附檔案，所以要是有，這次沒能傳送。需要的話我再試一次。",
		StreamCancelled:        "⏹️ 這次處理已取消。",

		InboxDetailLink: "查看詳情",
		InboxTypeLabels: map[string]string{
			"issue_assigned":     "任務指派",
			"mentioned":          "提及你",
			"status_changed":     "狀態變更",
			"comment_added":      "新留言",
			"new_comment":        "新留言",
			"reaction_added":     "表情反應",
			"task_failed":        "執行失敗",
			"unassigned":         "取消指派",
			"assignee_changed":   "指派人變更",
			"priority_changed":   "優先順序變更",
			"due_date_changed":   "截止日期變更",
			"start_date_changed": "開始日期變更",
		},
		InboxTypeFallback: "新訊息",
	},
	LocaleEn: {
		AgentOffline:         "⚠️ The agent is offline right now. Your message was received and will be handled once it's back.",
		AgentArchived:        "⚠️ This agent has been archived and can't reply. Please contact your workspace admin.",
		FreshPending:         "✅ Fresh start ready. Your next message stays in this chat but runs without the earlier context.",
		ChatStarted:          "✅ Started a new Multica chat. Your next message will enter it.",
		IssueUsage:           "Please include an issue title. Use:\n\n`/issue <title>`\n`[description]` (optional)",
		InvokeDenied:         "⚠️ You don't have permission to run this agent. Ask its owner if you need to use it.",
		BindingPromptPrefix:  "👋 Link your Multica account before we can talk:\n",
		BindingPromptSuffix:  "\n(the link is good for 15 minutes)",
		BindingPending:       "👋 I already sent you a link — it is just above, tap it to finish linking.",
		BindingSentPrivately: "👋 I've sent the link to your direct chat with me — tap it there to finish linking.",

		IssueCreatedPrefix:   "✅ Created ",
		IssueTitleSeparator:  " — ",
		IssueDuplicatePrefix: "⚠️ Not created — an active issue already covers this: ",

		StreamNoReply:          "(nothing to reply with this round)",
		StreamNoReplyWithFiles: "(no text this round — the files follow)",
		StreamNotStarted:       "Got it, but this one couldn't start processing.",
		StreamFailed:           "⚠️ That run didn't go through. Please try again.",
		MediaSendFailed:        "⚠️ I couldn't send one of the files. It is still here — say the word and I'll try again.",
		MediaSendUnknown:       "⚠️ WeCom never confirmed one of the files, so it may or may not have arrived. I won't resend it automatically in case that shows it twice — tell me if it isn't there and I'll send it again.",
		MediaLookupFailed:      "⚠️ I couldn't check whether this answer had a file with it, so if it did, it didn't go out. Say the word and I'll try again.",
		StreamCancelled:        "⏹️ That run was cancelled.",

		InboxDetailLink: "View details",
		InboxTypeLabels: map[string]string{
			"issue_assigned":     "Assigned",
			"mentioned":          "Mentioned",
			"status_changed":     "Status changed",
			"comment_added":      "New comment",
			"new_comment":        "New comment",
			"reaction_added":     "Reaction",
			"task_failed":        "Run failed",
			"unassigned":         "Unassigned",
			"assignee_changed":   "Assignee changed",
			"priority_changed":   "Priority changed",
			"due_date_changed":   "Due date changed",
			"start_date_changed": "Start date changed",
		},
		InboxTypeFallback: "Notification",
	},
}
