package api

import (
	"strings"
	"testing"

	"qingzhou/internal/store"
	"qingzhou/internal/telegram"
)

func TestApplyTplReplacesKnownLeavesUnknown(t *testing.T) {
	got := applyTpl("hello {{name}} {{missing}}", map[string]string{"name": "alice"})
	if got != "hello alice {{missing}}" {
		t.Fatalf("got %q", got)
	}
}

func TestTGBar(t *testing.T) {
	if !strings.Contains(tgBar(0, 0, true), "暂无") {
		t.Fatal("legacy unlimited flag must not turn zero into unlimited")
	}
	if !strings.Contains(tgBar(0, 0, false), "暂无") {
		t.Fatal("empty bar")
	}
	bar := tgBar(80, 100, false)
	if !strings.Contains(bar, "80%") {
		t.Fatalf("pct missing: %q", bar)
	}
	if strings.Count(bar, "▓")+strings.Count(bar, "░") != 10 {
		t.Fatalf("want 10 cells: %q", bar)
	}
}

func TestDefaultTGTemplateViewsDocumentVars(t *testing.T) {
	views := defaultTGTemplateViews()
	if len(views) != len(tgTplSpecs) {
		t.Fatalf("views=%d specs=%d", len(views), len(tgTplSpecs))
	}
	var sub map[string]any
	for _, raw := range views {
		v := raw
		if v["key"] == "sub" {
			sub = v
			break
		}
	}
	if sub == nil {
		t.Fatal("missing sub template view")
	}
	vars, _ := sub["vars"].([]J)
	found := false
	for _, v := range vars {
		if v["key"] == "url" {
			found = true
			if v["desc"] == "" {
				t.Fatal("url placeholder has no description")
			}
		}
	}
	if !found {
		t.Fatal("sub template is missing {{url}} in the documented vars")
	}
}

func TestNormalizeTGTplSetting(t *testing.T) {
	if got := normalizeTGTplSetting("tg_tpl_sub", defaultTGTemplates["sub"]); got != "" {
		t.Fatal("default should store as empty so upgrades keep flowing")
	}
	if got := normalizeTGTplSetting("tg_tpl_sub", "custom {{url}}"); got != "custom {{url}}" {
		t.Fatalf("custom = %q", got)
	}
	if got := normalizeTGTplSetting("smtp_host", "x"); got != "x" {
		t.Fatal("non-template keys must pass through")
	}
}

func TestCustomNotifyTemplate(t *testing.T) {
	a, st, inbox := newTelegramAPI(t)
	if err := st.SetSetting("tg_tpl_traffic_low", "LOW {{remain_pct}} {{remaining}}"); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser(store.NewUser{Username: "u1", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindTelegram(uid, 31, 31, "", ""); err != nil {
		t.Fatal(err)
	}
	insertPlan(t, st, uid, "计量", 100<<30, 90<<30, 0)
	a.sweepTelegramNotifies()
	if len(*inbox) != 1 || !strings.Contains((*inbox)[0].html, "LOW 10") {
		t.Fatalf("custom tpl = %#v", *inbox)
	}
	if strings.Contains((*inbox)[0].html, "流量不足") {
		t.Fatal("default copy leaked into an override")
	}
}

func TestPlanItemEscapeStillHoldsWithTemplate(t *testing.T) {
	a, st, inbox := newTelegramAPI(t)
	uid, err := st.CreateUser(store.NewUser{Username: "u1", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindTelegram(uid, 32, 32, "", ""); err != nil {
		t.Fatal(err)
	}
	insertPlan(t, st, uid, `<img src=x>`, 10<<30, 0, 0)
	a.handleTelegramUpdate(telegram.Update{UpdateID: 1, Message: &telegram.Message{
		From: &telegram.User{ID: 32},
		Chat: telegram.Chat{ID: 32, Type: "private"},
		Text: "/plan",
	}})
	if len(*inbox) != 1 {
		t.Fatalf("replies = %#v", *inbox)
	}
	if strings.Contains((*inbox)[0].html, "<img") {
		t.Fatalf("unescaped: %s", (*inbox)[0].html)
	}
	if !strings.Contains((*inbox)[0].html, "&lt;img src=x&gt;") {
		t.Fatalf("expected escaped name: %s", (*inbox)[0].html)
	}
}
