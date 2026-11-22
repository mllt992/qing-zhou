package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCallRedactsBotTokenOnNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	secretTok := "123456:SUPER_SECRET_BOT_TOKEN_XYZ"
	c := &Client{Token: secretTok, APIBase: srv.URL, HTTP: &http.Client{Timeout: 30 * time.Millisecond}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := GetMe(ctx, c)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if strings.Contains(err.Error(), secretTok) {
		t.Fatalf("bot token leaked in error string: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("expected redacted marker in error string: %s", err.Error())
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected wrapped DeadlineExceeded, got %v", err)
	}
}
