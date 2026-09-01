package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"qingzhou/internal/idgen"
	"qingzhou/internal/store"
	"qingzhou/internal/telegram"
)

const (
	telegramBindTTL     = 15 * time.Minute
	telegramPollTimeout = 25 // seconds; HTTP client timeout is 40s
)

// telegramToken is the effective bot token: env wins, then the encrypted
// setting. Empty means the bot is off — same shape as mailerConfigured.
func (a *API) telegramToken() string {
	if v := os.Getenv("QZ_TELEGRAM_BOT_TOKEN"); v != "" {
		return v
	}
	if a == nil || a.st == nil {
		return ""
	}
	v, _ := a.st.GetSetting("telegram_bot_token")
	return strings.TrimSpace(v)
}

func (a *API) telegramConfigured() bool { return a.telegramToken() != "" }

func (a *API) telegramClient() *telegram.Client { return a.telegramClientFor(a.telegramToken()) }

func (a *API) telegramClientFor(tok string) *telegram.Client {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return nil
	}
	if a.tgClientFn != nil {
		return a.tgClientFn(tok)
	}
	return &telegram.Client{Token: tok}
}

func (a *API) telegramUsername() string {
	if v := os.Getenv("QZ_TELEGRAM_BOT_USERNAME"); v != "" {
		return strings.TrimPrefix(strings.TrimSpace(v), "@")
	}
	if a == nil || a.st == nil {
		return ""
	}
	v, _ := a.st.GetSetting("telegram_bot_username")
	return strings.TrimPrefix(strings.TrimSpace(v), "@")
}

// refreshTelegramUsername calls getMe and caches the username so the panel can
// render t.me deep links without asking Telegram on every bind click.
func (a *API) refreshTelegramUsername(ctx context.Context, c *telegram.Client) (string, error) {
	me, err := telegram.GetMe(ctx, c)
	if err != nil {
		return "", err
	}
	name := strings.TrimPrefix(strings.TrimSpace(me.Username), "@")
	if os.Getenv("QZ_TELEGRAM_BOT_USERNAME") == "" {
		// Also clear an old cached name when a changed token reports no username.
		_ = a.st.SetSetting("telegram_bot_username", name)
	}
	return name, nil
}

func (a *API) siteName() string {
	if a == nil || a.st == nil {
		return "轻舟"
	}
	n, _ := a.st.GetSetting("site_name")
	if strings.TrimSpace(n) == "" {
		return "轻舟"
	}
	return n
}

// GET /api/user/telegram — bind status + notify prefs for the account page.
func (a *API) handleUserTelegram(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	if u == nil {
		fail(w, http.StatusUnauthorized, "未登录")
		return
	}
	enabled := a.telegramConfigured()
	out := J{
		"enabled":  enabled,
		"bound":    false,
		"username": "",
		"bot":      a.telegramUsername(),
	}
	if !enabled {
		ok(w, out)
		return
	}
	b, err := a.st.TelegramBindByUser(u.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取绑定失败")
		return
	}
	if b != nil {
		out["bound"] = true
		out["username"] = b.Username
		out["telegram_id"] = b.TelegramID
		out["notify_expiry"] = b.NotifyExpiry
		out["notify_traffic"] = b.NotifyTraffic
		out["bound_at"] = b.BoundAt
	}
	ok(w, out)
}

// POST /api/user/telegram/bind-token — mint a start payload and return the
// deep link. The token is single-use and short-lived; generating a new one
// voids the previous, so the URL on screen is the only live one.
func (a *API) handleTelegramBindToken(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	if u == nil {
		fail(w, http.StatusUnauthorized, "未登录")
		return
	}
	if !a.telegramConfigured() {
		fail(w, http.StatusServiceUnavailable, "管理员尚未配置 Telegram Bot")
		return
	}
	if a.resendRL != nil && !a.resendRL.allow(fmt.Sprintf("t%d", u.ID)) {
		fail(w, http.StatusTooManyRequests, "操作过于频繁，请稍后再试")
		return
	}
	bot := a.telegramUsername()
	// A stored token can change independently of the cached username. Resolve
	// getMe before minting every link so a newly pasted token never produces a
	// deep link to the previous bot. A host-pinned username remains authoritative.
	if os.Getenv("QZ_TELEGRAM_BOT_USERNAME") == "" {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		name, err := a.refreshTelegramUsername(ctx, a.telegramClient())
		cancel()
		if err != nil || name == "" {
			fail(w, http.StatusBadGateway, "无法联系 Telegram，请稍后重试或让管理员点一次「测试连接」")
			return
		}
		bot = name
	}
	token, err := idgen.RandToken(24)
	if err != nil {
		fail(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	if err := a.st.CreateTelegramBindToken(u.ID, token, telegramBindTTL); err != nil {
		fail(w, http.StatusInternalServerError, "创建绑定码失败")
		return
	}
	ok(w, J{
		"url":        "https://t.me/" + bot + "?start=" + token,
		"bot":        bot,
		"expires_in": int(telegramBindTTL.Seconds()),
	})
}

// POST /api/user/telegram/unbind
func (a *API) handleTelegramUnbind(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	if u == nil {
		fail(w, http.StatusUnauthorized, "未登录")
		return
	}
	b, _ := a.st.TelegramBindByUser(u.ID)
	if err := a.st.UnbindTelegram(u.ID); err != nil {
		fail(w, http.StatusInternalServerError, "解绑失败")
		return
	}
	if b != nil {
		go a.tgSend(b.ChatID, "已与「"+telegram.Escape(a.siteName())+"」解除绑定。")
	}
	ok(w, J{"message": "已解绑"})
}

// PUT /api/user/telegram/notify {notify_expiry, notify_traffic}
func (a *API) handleTelegramNotifyPrefs(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	if u == nil {
		fail(w, http.StatusUnauthorized, "未登录")
		return
	}
	var req struct {
		NotifyExpiry  *bool `json:"notify_expiry"`
		NotifyTraffic *bool `json:"notify_traffic"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	cur, err := a.st.TelegramBindByUser(u.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取绑定失败")
		return
	}
	if cur == nil {
		fail(w, http.StatusBadRequest, "尚未绑定 Telegram")
		return
	}
	expiry, traffic := cur.NotifyExpiry, cur.NotifyTraffic
	if req.NotifyExpiry != nil {
		expiry = *req.NotifyExpiry
	}
	if req.NotifyTraffic != nil {
		traffic = *req.NotifyTraffic
	}
	if err := a.st.SetTelegramNotify(u.ID, expiry, traffic); err != nil {
		fail(w, http.StatusInternalServerError, "保存失败")
		return
	}
	ok(w, J{"notify_expiry": expiry, "notify_traffic": traffic})
}

// POST /api/admin/settings/test-telegram — getMe with an optional unsaved
// candidate token and, if the admin is bound, drop a test message in that chat.
func (a *API) handleTestTelegram(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
	}
	token := strings.TrimSpace(req.Token)
	if token == "" || token == "***" {
		token = a.telegramToken()
	}
	c := a.telegramClientFor(token)
	if c == nil {
		fail(w, http.StatusBadRequest, "尚未填写 Bot Token")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	me, err := telegram.GetMe(ctx, c)
	name := ""
	if me != nil {
		name = strings.TrimPrefix(strings.TrimSpace(me.Username), "@")
	}
	if err != nil {
		fail(w, http.StatusBadGateway, "连接失败: "+err.Error())
		return
	}
	sent := false
	if uid, _ := r.Context().Value(ctxUserID).(int64); uid > 0 {
		if b, _ := a.st.TelegramBindByUser(uid); b != nil {
			msg := "这是来自「" + telegram.Escape(a.siteName()) + "」的测试消息。收到它说明 Bot 配置正确。"
			if err := telegram.SendHTML(ctx, c, b.ChatID, msg); err != nil {
				fail(w, http.StatusBadGateway, "Bot 正常，但测试消息发送失败: "+err.Error())
				return
			}
			sent = true
		}
	}
	ok(w, J{"username": name, "sent": sent})
}

// StartTelegram runs the long-poll loop and the expiry/traffic notify sweep.
func (a *API) StartTelegram(ctx context.Context) {
	go a.telegramPollLoop(ctx)
	go a.telegramNotifyLoop(ctx)
	go a.resumeManualNotifications()
}

func (a *API) telegramPollLoop(ctx context.Context) {
	var offset int64
	var lastToken string
	var lastMenuSignature string
	for {
		if ctx.Err() != nil {
			return
		}
		tok := a.telegramToken()
		if tok == "" {
			lastToken = ""
			lastMenuSignature = ""
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
			continue
		}
		c := &telegram.Client{Token: tok}
		if tok != lastToken {
			// A leftover webhook (this bot used elsewhere, or a previous mode)
			// makes getUpdates return 409. Clearing it is what makes polling
			// the thing that just works after pasting a token.
			dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			if err := telegram.DeleteWebhook(dctx, c); err != nil {
				log.Printf("telegram: deleteWebhook: %v", err)
			}
			if _, err := a.refreshTelegramUsername(dctx, c); err != nil {
				log.Printf("telegram: getMe: %v", err)
			} else {
				log.Printf("telegram: bot @%s polling", a.telegramUsername())
			}
			cancel()
			offset = 0
			lastToken = tok
			lastMenuSignature = ""
		}
		menuSignature := a.telegramMenuSignature()
		if menuSignature != lastMenuSignature {
			mctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			err := a.syncTelegramMenu(mctx, c)
			cancel()
			if err != nil {
				log.Printf("telegram: setMyCommands: %v", err)
			} else {
				lastMenuSignature = menuSignature
			}
		}
		uctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		updates, err := telegram.GetUpdates(uctx, c, offset, telegramPollTimeout)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			wait := 3 * time.Second
			var ae *telegram.APIError
			if errors.As(err, &ae) && ae.Unauthorized() {
				log.Printf("telegram: bot token rejected (401) — check 系统设置")
				wait = time.Minute
			} else {
				log.Printf("telegram: getUpdates: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			a.handleTelegramUpdate(u)
		}
	}
}

func (a *API) handleTelegramUpdate(u telegram.Update) {
	if u.Message == nil || u.Message.From == nil {
		return
	}
	msg := u.Message
	if msg.Chat.Type != "" && msg.Chat.Type != "private" {
		return
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}
	if a.tgRL != nil && !a.tgRL.allow(fmt.Sprintf("tg%d", msg.From.ID)) {
		return
	}
	cmd, arg := splitTelegramCommand(text)
	switch cmd {
	case "/start":
		a.tgCmdStart(msg, arg)
	case "/help", "帮助":
		a.tgCmdHelp(msg)
	case "/sub", "订阅":
		a.tgCmdSub(msg)
	case "/plan", "/plans", "套餐":
		a.tgCmdPlan(msg)
	case "/traffic", "流量":
		a.tgCmdTraffic(msg)
	case "/status", "状态":
		a.tgCmdStatus(msg)
	case "/unbind", "解绑":
		a.tgCmdUnbind(msg)
	default:
		if strings.HasPrefix(cmd, "/") {
			if item, ok := a.telegramCustomCommand(cmd); ok {
				a.tgCmdCustom(msg, item)
				return
			}
			a.tgSend(msg.Chat.ID, "未知命令。发送 /help 查看可用命令。")
		}
	}
}

// splitTelegramCommand pulls "/cmd@bot arg" into ("/cmd", "arg"). Plain
// Chinese keywords (订阅 / 套餐 / 流量) are returned as the command with no
// leading slash so the switch above can accept both shapes.
func splitTelegramCommand(text string) (cmd, arg string) {
	parts := strings.SplitN(text, " ", 2)
	cmd = parts[0]
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}
	if i := strings.IndexByte(cmd, '@'); i > 0 {
		cmd = cmd[:i]
	}
	return cmd, arg
}

func (a *API) tgCmdStart(msg *telegram.Message, token string) {
	from := msg.From
	if token == "" {
		if b, _ := a.st.TelegramBindByTelegramID(from.ID); b != nil {
			a.st.TouchTelegramChat(from.ID)
			a.tgSend(msg.Chat.ID, a.tgWelcome(b.UserID))
			return
		}
		a.tgSend(msg.Chat.ID, "请先在「"+telegram.Escape(a.siteName())+"」面板的<b>账户设置</b>里点击绑定，再打开机器人。")
		return
	}
	userID, ok, err := a.st.BindTelegramWithToken(token, from.ID, msg.Chat.ID, from.Username, from.FirstName)
	if errors.Is(err, store.ErrTelegramTaken) {
		a.tgSend(msg.Chat.ID, "这个 Telegram 已绑定其他账号。请先 /unbind 解绑，或在原账号的面板里解绑。")
		return
	}
	if err != nil {
		log.Printf("telegram: bind token: %v", err)
		a.tgSend(msg.Chat.ID, "绑定失败，请稍后重试。")
		return
	}
	if !ok {
		a.tgSend(msg.Chat.ID, "绑定码无效或已过期，请回面板重新生成。")
		return
	}
	u, _ := a.st.UserByID(userID)
	if u == nil {
		a.tgSend(msg.Chat.ID, "对应账号已不存在。")
		return
	}
	a.tgSend(msg.Chat.ID, a.renderTGBound(u.Username))
}

func (a *API) tgCmdHelp(msg *telegram.Message) {
	name := ""
	if msg.From != nil {
		if b, _ := a.st.TelegramBindByTelegramID(msg.From.ID); b != nil {
			if u, _ := a.st.UserByID(b.UserID); u != nil {
				name = u.Username
			}
		}
	}
	a.tgSend(msg.Chat.ID, a.renderTGHelp(name))
}

func (a *API) tgWelcome(userID int64) string {
	name := ""
	if u, _ := a.st.UserByID(userID); u != nil {
		name = u.Username
	}
	return a.renderTGBound(name)
}

func (a *API) tgBoundUser(msg *telegram.Message) (*store.User, *store.TelegramBind, bool) {
	b, err := a.st.TelegramBindByTelegramID(msg.From.ID)
	if err != nil || b == nil {
		a.tgSend(msg.Chat.ID, "尚未绑定。请先在面板「账户设置」里生成绑定链接。")
		return nil, nil, false
	}
	a.st.TouchTelegramChat(msg.From.ID)
	u, err := a.st.UserByID(b.UserID)
	if err != nil || u == nil {
		a.tgSend(msg.Chat.ID, "对应账号已不存在，绑定已失效。")
		_ = a.st.UnbindTelegram(b.UserID)
		return nil, nil, false
	}
	if u.Status == "banned" {
		a.tgSend(msg.Chat.ID, "账号已被禁用。")
		return nil, nil, false
	}
	return u, b, true
}

func (a *API) tgCmdSub(msg *telegram.Message) {
	u, _, ok := a.tgBoundUser(msg)
	if !ok {
		return
	}
	u = a.refreshAfterPromotion(u, a.advanceQueueOnRead(u.ID))
	u = a.ensureSubToken(u)
	url := a.subURLAt(u)
	if url == "" {
		a.tgSend(msg.Chat.ID, "暂时无法生成订阅地址（面板未配置访问地址，或账号还没有订阅令牌）。请打开面板查看。")
		return
	}
	a.tgSend(msg.Chat.ID, a.renderTGSub(u.Username, url))
}

func (a *API) tgCmdPlan(msg *telegram.Message) {
	u, _, ok := a.tgBoundUser(msg)
	if !ok {
		return
	}
	u = a.refreshAfterPromotion(u, a.advanceQueueOnRead(u.ID))
	a.tgSend(msg.Chat.ID, a.renderTGPlans(u.ID, u.Username))
}

func (a *API) tgCmdTraffic(msg *telegram.Message) {
	u, _, ok := a.tgBoundUser(msg)
	if !ok {
		return
	}
	u = a.refreshAfterPromotion(u, a.advanceQueueOnRead(u.ID))
	a.tgSend(msg.Chat.ID, a.renderTGTraffic(u.ID, u.Username))
}

func (a *API) tgCmdStatus(msg *telegram.Message) {
	u, _, ok := a.tgBoundUser(msg)
	if !ok {
		return
	}
	u = a.refreshAfterPromotion(u, a.advanceQueueOnRead(u.ID))
	a.tgSend(msg.Chat.ID, a.renderTGStatus(u.ID, u.Username))
}

func (a *API) tgCmdUnbind(msg *telegram.Message) {
	uid, ok, err := a.st.UnbindTelegramByTelegramID(msg.From.ID)
	if err != nil {
		a.tgSend(msg.Chat.ID, "解绑失败，请稍后重试。")
		return
	}
	if !ok {
		a.tgSend(msg.Chat.ID, "当前没有绑定。")
		return
	}
	_ = uid
	a.tgSend(msg.Chat.ID, "已解绑。之后可在面板「账户设置」里重新绑定。")
}

func formatPlanTraffic(p planView) string {
	return fmt.Sprintf("已用 %s / %s，剩余 %s", fmtBytes(p.Used), fmtBytes(p.TrafficLimit), fmtBytes(p.Remaining))
}

func planStatusLabel(s string) string {
	switch s {
	case "active":
		return "生效中"
	case "queued":
		return "排队中"
	case "expired":
		return "已过期"
	case "exhausted":
		return "已用尽"
	default:
		return s
	}
}

func fmtUnix(ts int64) string {
	if ts <= 0 {
		return "—"
	}
	return time.Unix(ts, 0).Local().Format("2006-01-02 15:04")
}

// subURLAt builds the subscription address from the configured public base,
// without an incoming HTTP request — bot commands have none.
func (a *API) subURLAt(u *store.User) string {
	base := a.siteBase()
	if base == "" || u == nil || !u.SubToken.Valid || u.SubToken.String == "" {
		return ""
	}
	return base + "/sub/" + u.SubToken.String
}

func (a *API) tgSend(chatID int64, html string) error {
	if a.tgSendFn != nil {
		return a.tgSendFn(chatID, html)
	}
	c := a.telegramClient()
	if c == nil {
		return errors.New("telegram bot not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := telegram.SendHTML(ctx, c, chatID, html); err != nil {
		log.Printf("telegram: send to %d: %v", chatID, err)
		return err
	}
	return nil
}
