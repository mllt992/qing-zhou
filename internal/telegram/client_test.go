package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEscape(t *testing.T) {
	in := `a<b>&"c"`
	got := Escape(in)
	if strings.Contains(got, "<") || strings.Contains(got, ">") {
		t.Fatalf("unescaped brackets: %q", got)
	}
	if !strings.Contains(got, "&amp;") || !strings.Contains(got, "&lt;") {
		t.Fatalf("escape = %q", got)
	}
}

func TestCallRejectsHTTPErrorAndOversizedResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"HTTP error", 502, `{"ok":true,"result":{}}`},
		{"oversized", 200, `{"ok":true,"result":{}}` + strings.Repeat(" ", 1<<20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			c := &Client{Token: "fixture", APIBase: srv.URL, HTTP: srv.Client()}
			if _, err := GetMe(context.Background(), c); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

func TestGetMeAndSend(t *testing.T) {
	var gotSend map[string]any
	var gotCommands map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"舟","username":"qingzhou_bot"}}`)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			_ = json.NewDecoder(r.Body).Decode(&gotSend)
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":7}}`)
		case strings.HasSuffix(r.URL.Path, "/setMyCommands"):
			_ = json.NewDecoder(r.Body).Decode(&gotCommands)
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &Client{Token: "TEST:token", APIBase: srv.URL, HTTP: srv.Client()}
	me, err := GetMe(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if me.Username != "qingzhou_bot" {
		t.Fatalf("username = %q", me.Username)
	}
	if err := SendHTML(context.Background(), c, 42, "<b>hi</b>"); err != nil {
		t.Fatal(err)
	}
	if gotSend["chat_id"] != float64(42) || gotSend["parse_mode"] != "HTML" {
		t.Fatalf("send payload = %#v", gotSend)
	}
	if gotSend["disable_web_page_preview"] != true {
		t.Fatal("subscription URLs would be preview-fetched")
	}
	if err := SetCommands(context.Background(), c, []BotCommand{{Command: "help", Description: "Help"}}); err != nil {
		t.Fatal(err)
	}
	commands, ok := gotCommands["commands"].([]any)
	if !ok || len(commands) != 1 || commands[0].(map[string]any)["command"] != "help" {
		t.Fatalf("setMyCommands payload = %#v", gotCommands)
	}
}

func TestUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	}))
	defer srv.Close()
	c := &Client{Token: "bad", APIBase: srv.URL, HTTP: srv.Client()}
	_, err := GetMe(context.Background(), c)
	ae, ok := err.(*APIError)
	if !ok || !ae.Unauthorized() {
		t.Fatalf("err = %v (%T)", err, err)
	}
}
