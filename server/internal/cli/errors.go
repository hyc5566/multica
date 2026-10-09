package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
)

// ErrorKind is a coarse, user-facing classification of an error. The CLI's
// many internal error strings ("resolve issue: ...", raw net/http messages,
// JSON bodies) are not meaningful to end users; FormatError collapses them
// into one of these kinds and renders a friendly, localized message.
//
// The zero value is intentionally KindNetworkTimeout-adjacent only by index;
// always classify explicitly rather than relying on the zero value.
type ErrorKind int

const (
	// Network / transport layer (errors returned by http.Client.Do).
	KindNetworkTimeout ErrorKind = iota // context deadline exceeded / i/o timeout
	KindNetworkDNS                      // no such host
	KindNetworkRefused                  // connection refused
	KindNetworkTLS                      // x509 / tls handshake failures
	KindNetworkOffline                  // catch-all: host unreachable, reset, etc.
	// KindNetworkStalled is a transfer that stopped producing bytes (see
	// StallError). Distinct from KindNetworkTimeout because the remedy is
	// different: a timeout says "this took too long", a stall says "this went
	// quiet", and only the latter is unaffected by raising a time limit.
	KindNetworkStalled
	// KindNetworkTLSHandshakeTimeout is a TLS handshake that never completed
	// after the TCP connection opened. Distinct from KindNetworkTimeout
	// because the remedy is different: the request budget
	// (MULTICA_HTTP_TIMEOUT) does not govern the handshake, and the usual
	// cause is a network path that drops a ClientHello spanning two TCP
	// packets — which every Go client sends by default since Go 1.24 (the
	// post-quantum key share makes it ~1.5 KB) while curl on the same machine
	// gets through (GH #8654).
	KindNetworkTLSHandshakeTimeout

	// HTTP status layer.
	KindAuthRequired // 401
	// KindTaskTokenRejected is a 401 on a task-scoped token. HTTPError.Kind()
	// never returns it — the status code alone cannot tell the two apart — so
	// it is selected in userMessage, where the credential the request actually
	// sent is known. Exit classification is unchanged: still an auth failure.
	KindTaskTokenRejected
	KindForbidden   // 403
	KindNotFound    // 404
	KindConflict    // 409
	KindValidation  // 400 / 422
	KindRateLimited // 429
	KindServerError // 5xx

	// Anything we could not classify.
	KindUnknown
)

// Tiered process exit codes. Stable so users can branch on them in scripts.
const (
	ExitGeneric    = 1 // anything not covered below
	ExitNetwork    = 2 // any KindNetwork*
	ExitAuth       = 3 // 401 / 403
	ExitNotFound   = 4 // 404
	ExitValidation = 5 // 400 / 422
)

// IsNetwork reports whether the kind is a transport-layer failure.
func (k ErrorKind) IsNetwork() bool {
	switch k {
	case KindNetworkTimeout, KindNetworkDNS, KindNetworkRefused, KindNetworkTLS, KindNetworkOffline, KindNetworkStalled, KindNetworkTLSHandshakeTimeout:
		return true
	default:
		return false
	}
}

// String returns a stable, snake_case identifier for the kind. It is used in
// --debug output and is safe to log or branch on; it is not user-facing copy
// (see kindMessages / messageFor for that).
func (k ErrorKind) String() string {
	switch k {
	case KindNetworkTimeout:
		return "network_timeout"
	case KindNetworkDNS:
		return "network_dns"
	case KindNetworkRefused:
		return "network_refused"
	case KindNetworkTLS:
		return "network_tls"
	case KindNetworkOffline:
		return "network_offline"
	case KindNetworkStalled:
		return "network_stalled"
	case KindNetworkTLSHandshakeTimeout:
		return "network_tls_handshake_timeout"
	case KindAuthRequired:
		return "auth_required"
	case KindTaskTokenRejected:
		return "task_token_rejected"
	case KindForbidden:
		return "forbidden"
	case KindNotFound:
		return "not_found"
	case KindConflict:
		return "conflict"
	case KindValidation:
		return "validation"
	case KindRateLimited:
		return "rate_limited"
	case KindServerError:
		return "server_error"
	case KindUnknown:
		return "unknown"
	default:
		return fmt.Sprintf("ErrorKind(%d)", int(k))
	}
}

// NetworkError wraps a transport-layer error (the error returned by
// http.Client.Do, before any HTTP status is available). It strips the raw
// URL out of the user-facing message while preserving the original error for
// --debug output and errors.Is/As inspection.
type NetworkError struct {
	Kind ErrorKind
	Op   string // e.g. "GET /api/issues/abc" — shown only in --debug
	Err  error  // the original net/http error
}

func (e *NetworkError) Error() string {
	if e.Op != "" {
		return fmt.Sprintf("%s: %s", e.Op, e.Err.Error())
	}
	return e.Err.Error()
}

func (e *NetworkError) Unwrap() error { return e.Err }

// UserMessageError attaches a command-specific, user-facing message to an
// underlying error. FormatError shows Msg verbatim (in preference to the
// generic kind-based copy it would otherwise derive from a wrapped
// *NetworkError / *HTTPError), so command-level guidance — e.g. a `multica
// login` failure that is more helpful than the generic 401/timeout line — is
// visible in the default (non-debug) output.
//
// It preserves Unwrap(), so ExitCodeFor still classifies by the underlying
// typed error and --debug still prints the full original chain.
type UserMessageError struct {
	Msg string
	Err error
}

func (e *UserMessageError) Error() string {
	if e.Err != nil {
		return e.Msg + ": " + e.Err.Error()
	}
	return e.Msg
}

func (e *UserMessageError) Unwrap() error { return e.Err }

// WithUserMessage wraps err with a user-facing message that FormatError will
// surface by default. It returns nil when err is nil so it can be used inline
// in a `return` without an extra check.
func WithUserMessage(msg string, err error) error {
	if err == nil {
		return nil
	}
	return &UserMessageError{Msg: msg, Err: err}
}

// WithUserMessageUnlessNetwork is WithUserMessage for a command whose custom
// copy explains an HTTP-level refusal — a rejected token, a server that would
// not issue one — and would be wrong for a transport failure. When err is a
// *NetworkError it is returned unchanged, so FormatError renders the
// kind-based copy, which is the only place that names the actual remedy
// (DNS, proxy, TLS handshake).
//
// `multica login` needs this: with WithUserMessage, a TLS handshake that
// never completed was reported as "the server could not issue an access
// token" and "make sure the token is valid and not expired", and nothing in
// the default output pointed at the network (GH #8654).
func WithUserMessageUnlessNetwork(msg string, err error) error {
	if err == nil {
		return nil
	}
	var netErr *NetworkError
	if errors.As(err, &netErr) {
		return err
	}
	return &UserMessageError{Msg: msg, Err: err}
}

// Kind maps an HTTPError's status code onto an ErrorKind.
func (e *HTTPError) Kind() ErrorKind {
	switch e.StatusCode {
	case 401:
		return KindAuthRequired
	case 403:
		return KindForbidden
	case 404:
		return KindNotFound
	case 409:
		return KindConflict
	case 400, 422:
		return KindValidation
	case 429:
		return KindRateLimited
	default:
		if e.StatusCode >= 500 {
			return KindServerError
		}
		return KindUnknown
	}
}

// classifyNetworkError inspects a transport-layer error and returns the
// matching network ErrorKind. It prefers typed inspection (errors.As /
// errors.Is) and falls back to string matching for cases the standard library
// does not expose as distinct types.
func classifyNetworkError(err error) ErrorKind {
	if err == nil {
		return KindUnknown
	}

	// A stalled transfer is checked first: the guard implements it by
	// canceling the request context, so the underlying error would otherwise
	// read as a generic cancellation and lose the reason.
	var stalled *StallError
	if errors.As(err, &stalled) {
		return KindNetworkStalled
	}

	// Timeouts (context deadline or socket i/o timeout).
	if errors.Is(err, context.DeadlineExceeded) {
		return KindNetworkTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		// net/http reports a TLS handshake that never completed with an
		// unexported type that also satisfies Timeout(), so only its message
		// tells it apart from a socket timeout. The remedy differs (see
		// KindNetworkTLSHandshakeTimeout), so it must not be folded into the
		// generic timeout. Error() is only consulted here, on a timeout: the
		// typed x509 checks below must keep running before any message is
		// rendered, because a hostname error renders its certificate.
		if isTLSHandshakeTimeout(err) {
			return KindNetworkTLSHandshakeTimeout
		}
		return KindNetworkTimeout
	}

	// DNS resolution failures.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return KindNetworkDNS
	}

	// TLS / certificate failures.
	var certVerifyErr *tls.CertificateVerificationError
	if errors.As(err, &certVerifyErr) {
		return KindNetworkTLS
	}
	var unknownAuthorityErr x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthorityErr) {
		return KindNetworkTLS
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return KindNetworkTLS
	}
	var certInvalidErr x509.CertificateInvalidError
	if errors.As(err, &certInvalidErr) {
		return KindNetworkTLS
	}

	// Connection refused.
	if errors.Is(err, syscall.ECONNREFUSED) {
		return KindNetworkRefused
	}

	// String fallbacks for anything not surfaced as a typed error.
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "tls handshake timeout"):
		return KindNetworkTLSHandshakeTimeout
	case strings.Contains(msg, "context deadline exceeded"), strings.Contains(msg, "timeout"), strings.Contains(msg, "timed out"):
		return KindNetworkTimeout
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "server misbehaving"), strings.Contains(msg, "name resolution"):
		return KindNetworkDNS
	case strings.Contains(msg, "connection refused"):
		return KindNetworkRefused
	case strings.Contains(msg, "x509"), strings.Contains(msg, "certificate"), strings.Contains(msg, "tls"):
		return KindNetworkTLS
	}
	return KindNetworkOffline
}

// isTLSHandshakeTimeout reports whether err is net/http's TLS handshake
// timeout, which is only identifiable by its message.
func isTLSHandshakeTimeout(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "tls handshake timeout")
}

// wrapTransport converts a raw transport error returned by http.Client.Do
// into a *NetworkError. It returns nil when err is nil so call sites can
// reassign unconditionally:
//
//	resp, err := c.HTTPClient.Do(req)
//	err = wrapTransport(req, err)
//	if err != nil { return err }
func wrapTransport(req *http.Request, err error) error {
	if err == nil {
		return nil
	}
	op := ""
	if req != nil && req.URL != nil {
		op = req.Method + " " + req.URL.Path
	}
	return &NetworkError{Kind: classifyNetworkError(err), Op: op, Err: err}
}

// wrapBodyRead classifies an error raised while reading or decoding a
// response body.
//
// wrapTransport only sees errors from http.Client.Do, which returns as soon as
// the response headers arrive. Everything that goes wrong afterwards — the
// case #7498 actually reports, a body that never finishes arriving — surfaces
// out of the JSON decoder instead, and used to reach the user as a raw
// "context deadline exceeded ... while reading body". A transport failure is
// still a transport failure when the decoder is the one that notices it; a
// genuine malformed-JSON error is returned unchanged.
//
// io.ErrUnexpectedEOF is deliberately on the network side of that line. It is
// what the decoder reports for a body that simply stops — a dropped
// connection, a proxy cutting the response — which is vastly the more common
// cause than a server that emits syntactically truncated JSON. Well-formed
// nonsense (the ordinary server bug) raises *json.SyntaxError and is left
// alone.
func wrapBodyRead(req *http.Request, err error) error {
	if err == nil {
		return nil
	}
	var stalled *StallError
	if errors.As(err, &stalled) {
		return wrapTransport(req, err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return wrapTransport(req, err)
	}
	return err
}

// Language is the language FormatError renders messages in.
type Language int

const (
	LangEN Language = iota
	LangZH
)

// DetectLanguage chooses the output language from the environment. English is
// the default (matching the CLI's help output); a Chinese locale in LC_ALL,
// LC_MESSAGES, or LANG (in that precedence order) switches to Chinese.
func DetectLanguage() Language {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
		if v == "" {
			continue
		}
		if strings.HasPrefix(v, "zh") {
			return LangZH
		}
		// First locale variable that is set wins; if it is not Chinese we
		// fall through to English without consulting lower-precedence vars.
		return LangEN
	}
	return LangEN
}

// kindMessages holds the {English, Chinese} user-facing message for each kind.
var kindMessages = map[ErrorKind][2]string{
	KindNetworkTimeout: {
		"Request timed out: the server did not respond in time. Check your network connection or try again later. You can raise the limit with MULTICA_HTTP_TIMEOUT.",
		"請求逾時：伺服器未在規定時間內回應。請檢查網路連線或稍後重試。可透過 MULTICA_HTTP_TIMEOUT 調高逾時時間。",
	},
	KindNetworkTLSHandshakeTimeout: {
		"TLS handshake timed out: the connection to the Multica server opened, but the secure handshake never completed. Something on this network path (security software, a VPN, a router, or a firewall) is probably dropping large TLS handshakes; curl or a browser on the same machine may still work. Retry with the environment variable GODEBUG=tlsmlkem=0 set, and keep it set for the CLI and the daemon if that fixes it. MULTICA_HTTP_TIMEOUT does not affect the handshake.",
		"TLS 握手逾時：已連上 Multica 伺服器，但安全握手一直沒有完成。通常是網路路徑上的安全軟體、VPN、路由器或防火牆丟棄了較大的 TLS 握手包，同一臺機器上的 curl 或瀏覽器可能仍然正常。請設定環境變數 GODEBUG=tlsmlkem=0 後重試；若因此恢復，請為 CLI 和守護程序長期保留該設定。MULTICA_HTTP_TIMEOUT 對握手無效。",
	},
	KindNetworkStalled: {
		"Transfer stalled: the connection stopped sending data before the response was complete. Check your network connection or try again. You can raise the no-progress budget with MULTICA_HTTP_STALL_TIMEOUT.",
		"傳輸中斷：回應尚未接收完畢，連線就停止傳送資料。請檢查網路連線或重試。可透過 MULTICA_HTTP_STALL_TIMEOUT 調高無進展等待時間。",
	},
	KindNetworkDNS: {
		"Could not resolve the Multica server address. Check your network connection or the --server-url setting.",
		"無法解析 Multica 伺服器位址。請檢查網路連線或 --server-url 設定。",
	},
	KindNetworkRefused: {
		"Could not connect to the Multica server. Make sure the server address is correct and reachable.",
		"無法連線到 Multica 伺服器。請確認伺服器位址正確且網路可達。",
	},
	KindNetworkTLS: {
		"Could not establish a secure connection to the Multica server (TLS/certificate error). Check your system clock and CA certificates.",
		"無法與 Multica 伺服器建立安全連線（TLS/憑證錯誤）。請檢查系統時間和 CA 憑證。",
	},
	KindNetworkOffline: {
		"Could not reach the Multica server. Check your network connection.",
		"無法存取 Multica 伺服器。請檢查網路連線。",
	},
	KindAuthRequired: {
		"Your session has expired or you are not signed in. Run `multica login` to sign in again. On a self-hosted or non-OAuth setup, ask your administrator for valid credentials.",
		"登入已過期或尚未登入。請執行 `multica login` 重新登入。自行架設或非 OAuth 情境請聯絡管理員取得有效憑證。",
	},
	KindTaskTokenRejected: {
		"This task token was rejected and is no longer usable. Stop here: do not retry, and do not fall back to a profile or member credential, because anything done with one would run as that person rather than as this task. Only the runtime that started this task can supply a valid task token.",
		"這個 task token 已被拒絕，不再可用。請到此為止：不要重試，也不要改用 profile 或成員憑證 —— 用它們執行的任何操作都會以那個成員的身分執行，而不是以這次 task 的身分執行。只有啟動這次 task 的執行環境才能提供有效的 task token。",
	},
	KindForbidden: {
		"You do not have permission to access this resource. Check that you are in the right workspace, or ask an administrator to grant access.",
		"無權存取該資源。請確認目前 workspace 是否正確，或聯絡管理員授予權限。",
	},
	KindNotFound: {
		"The requested resource was not found. Check the ID, or run the matching `list` command to see what exists in this workspace.",
		"未找到請求的資源。請核對 ID，或執行對應的 list 指令查看目前 workspace 中已有的內容。",
	},
	KindConflict: {
		"The request conflicts with the current state of the resource (it may already exist or have changed since you last fetched it). Re-fetch the latest state and try again.",
		"請求與資源的目前狀態衝突（可能已存在，或自上次取得後已被修改）。請重新取得最新狀態後再試。",
	},
	KindValidation: {
		"The request was invalid. Check the values you provided; run the command with --help to see the expected format.",
		"請求無效。請檢查所填寫的參數；可用 --help 查看期望的格式。",
	},
	KindRateLimited: {
		"Too many requests. Please wait a moment and try again; if this keeps happening, reduce how frequently you call the API.",
		"請求過於頻繁。請稍候重試；若持續出現，請降低 API 呼叫頻率。",
	},
	KindServerError: {
		"The Multica service is temporarily unavailable (server error). Please try again later; if it persists, contact support. Re-run with --debug to see the raw server response.",
		"Multica 服務暫時不可用（伺服器錯誤）。請稍後重試；若持續出現請聯絡支援。可加 --debug 查看伺服器原始回應。",
	},
	KindUnknown: {
		"An unexpected error occurred.",
		"發生未知錯誤。",
	},
}

// serverMessagePrefixes lists the kinds whose response body carries a
// hand-written, actionable message worth showing instead of the generic
// template, with the {English, Chinese} lead-in used to introduce it.
//
// 409 belongs here because the generic conflict copy is not merely vague, it
// points the wrong way: it reads as a transient race ("re-fetch the latest
// state and try again") while every conflict this API returns is a
// deterministic refusal that names its own fix ("a skill with this name
// already exists", "set parent_id (--parent) to <id>"). Agents took the retry
// hint literally and burned hours re-sending an unchanged request (GH #6264,
// GH #5948), and the server-side wording added in MUL-4417 never reached them.
var serverMessagePrefixes = map[ErrorKind][2]string{
	KindValidation: {"Invalid request: ", "請求無效："},
	KindConflict:   {"Request conflict: ", "請求衝突："},
}

// messageFor returns the localized message for a kind.
func messageFor(kind ErrorKind, lang Language) string {
	m, ok := kindMessages[kind]
	if !ok {
		m = kindMessages[KindUnknown]
	}
	if lang == LangZH {
		return m[1]
	}
	return m[0]
}

// FormatError translates an error into a single user-facing line (or a
// detailed multi-line block when debug is set). It is the only user-facing
// translation entry point and is meant to be called once, at the top level
// (main.go), on the error bubbling up from a command.
//
// When debug is false it skips the internal verb chain ("resolve issue: ...")
// and the raw URL/JSON body, showing only the friendly message. When debug is
// true (or MULTICA_DEBUG is set) it additionally prints the full original
// error chain for troubleshooting.
func FormatError(err error, debug bool) string {
	if err == nil {
		return ""
	}
	lang := DetectLanguage()
	base := userMessage(err, lang)
	if debug || debugEnabled() {
		return base + "\n\n" + debugDetail(err)
	}
	return base
}

// userMessage produces the friendly message for the root cause of err.
func userMessage(err error, lang Language) string {
	// A command-supplied user-facing message takes precedence over the generic
	// kind-based copy, so command-specific guidance (e.g. sign-in failures) is
	// visible by default. Unwrap() is preserved, so ExitCodeFor and --debug
	// still see the underlying typed error.
	var um *UserMessageError
	if errors.As(err, &um) {
		return um.Msg
	}

	// Transport-layer failure.
	var netErr *NetworkError
	if errors.As(err, &netErr) {
		return messageFor(netErr.Kind, lang)
	}

	// HTTP status failure.
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		kind := httpErr.Kind()
		// A 401 on a task token is not a login problem, and the generic copy
		// below is the wrong instruction for whoever reads it: it says to sign
		// in again or ask an administrator for valid credentials. An
		// autonomous agent can act on that, and one did — after its task token
		// stopped working mid-run it read the daemon owner's profile PAT and
		// kept going under the member's identity (GH #7522).
		//
		// What the copy must not do is guess *why*. The usual cause is the
		// task reaching a terminal state, but the same 401 covers a malformed
		// token, one sent to the wrong server, and one dropped by an unrelated
		// cleanup. "Stop" is true in every one of those cases; "the task
		// finished" is not.
		if kind == KindAuthRequired && httpErr.TaskScoped {
			return messageFor(KindTaskTokenRejected, lang)
		}
		// Validation and conflict errors carry a useful server-provided
		// message; surface it instead of the generic line. A body we cannot
		// recognize still falls back to the template, so this never dumps a
		// raw response at the user.
		if prefix, ok := serverMessagePrefixes[kind]; ok {
			if serverMsg := extractServerMessage(httpErr.Body); serverMsg != "" {
				if lang == LangZH {
					return prefix[1] + serverMsg
				}
				return prefix[0] + serverMsg
			}
		}
		return messageFor(kind, lang)
	}

	// Not a recognized typed error: this is typically a local/business error
	// whose message is already meant for the user (e.g. a missing argument or
	// a validation message constructed in a command). Show it as-is.
	return strings.TrimSpace(err.Error())
}

// extractServerMessage tries to pull a human-readable message out of a JSON
// error body like {"error":"..."} or {"message":"..."}. Returns "" if the
// body is not JSON or has no recognizable message field.
//
// A few endpoints put a stable machine code in "error" and the prose in
// "message" (the issue-table cursor responses do this). Prose always wins;
// a bare code is kept only as a last resort so the user still gets something
// greppable when no sentence is on offer.
func extractServerMessage(body string) string {
	body = strings.TrimSpace(body)
	if body == "" || body[0] != '{' {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return ""
	}
	var code string
	for _, key := range []string{"error", "message", "detail", "title"} {
		v, ok := parsed[key]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if looksLikeMachineCode(s) {
			if code == "" {
				code = s
			}
			continue
		}
		return s
	}
	return code
}

// ServerErrorCode returns the stable `code` a server error body carries, or ""
// when err is not an HTTP failure or the body has no code.
//
// It exists so a command can recognize ONE specific refusal and print guidance
// for it without matching on the English sentence, which changes with copy edits
// and disappears under translation. Statuses whose generic copy is deliberately
// vague — 403 above all, where naming the cause could confirm a resource exists —
// keep that copy for every code a command has not explicitly opted into.
func ServerErrorCode(err error) string {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return ""
	}
	body := strings.TrimSpace(httpErr.Body)
	if body == "" || body[0] != '{' {
		return ""
	}
	var parsed struct {
		Code string `json:"code"`
	}
	if jsonErr := json.Unmarshal([]byte(body), &parsed); jsonErr != nil {
		return ""
	}
	if !looksLikeMachineCode(parsed.Code) {
		return ""
	}
	return parsed.Code
}

// looksLikeMachineCode reports whether s is a bare identifier such as
// "cursor_query_mismatch" rather than a sentence meant for a person. The test
// is deliberately narrow — lowercase word characters only — so that ordinary
// prose in any language, including Chinese without ASCII spaces, is never
// mistaken for a code.
func looksLikeMachineCode(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// debugDetail renders the full original error chain plus any structured
// details from typed errors, for --debug / MULTICA_DEBUG output.
func debugDetail(err error) string {
	var sb strings.Builder
	sb.WriteString("[debug] ")
	sb.WriteString(err.Error())

	var netErr *NetworkError
	if errors.As(err, &netErr) {
		fmt.Fprintf(&sb, "\n[debug] network: op=%q kind=%s cause=%v", netErr.Op, netErr.Kind, netErr.Err)
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		fmt.Fprintf(&sb, "\n[debug] http: %s %s status=%d body=%s",
			httpErr.Method, httpErr.Path, httpErr.StatusCode, strings.TrimSpace(httpErr.Body))
	}
	return sb.String()
}

// debugEnabled reports whether MULTICA_DEBUG requests debug output.
func debugEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MULTICA_DEBUG"))) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// ExitCodeFor maps an error onto a tiered process exit code so callers can
// branch in scripts: network=2, auth(401/403)=3, not-found(404)=4,
// validation(400/422)=5, everything else=1.
func ExitCodeFor(err error) int {
	if err == nil {
		return 0
	}

	var netErr *NetworkError
	if errors.As(err, &netErr) {
		return ExitNetwork
	}

	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.Kind() {
		case KindAuthRequired, KindForbidden:
			return ExitAuth
		case KindNotFound:
			return ExitNotFound
		case KindValidation:
			return ExitValidation
		default:
			return ExitGeneric
		}
	}

	return ExitGeneric
}
