package api

import (
	"encoding/base64"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"qingzhou/internal/store"
	"qingzhou/internal/subconv"
)

func TestSubscriptionExternalRemark(t *testing.T) {
	a, st := newUserEditAPI(t)
	gid, err := st.CreateGroup(store.NodeGroup{Name: "free"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("free_group_id", strconv.FormatInt(gid, 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(store.NewUser{Username: "remark-user", PasswordHash: "x", SubToken: "REMARK_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	for _, remark := range []string{"", "HK-VIP"} {
		if _, err := st.CreateNode(store.Node{Type: "external", Name: "admin-name", Remark: remark,
			ShareLink: "trojan://secret@example.com:443#original", Enabled: true, GroupIDs: []int64{gid}}); err != nil {
			t.Fatal(err)
		}
	}
	router := a.Router()
	for _, format := range []string{"base64", "clash", "singbox"} {
		t.Run(format, func(t *testing.T) {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", "/sub/REMARK_TOKEN?format="+format, nil))
			if w.Code != 200 {
				t.Fatalf("subscription: %d %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			if format == "base64" {
				decoded, err := base64.StdEncoding.DecodeString(body)
				if err != nil {
					t.Fatal(err)
				}
				body = string(decoded)
				if len(subconv.ParseLinks(strings.Split(body, "\n"))) != 2 {
					t.Fatalf("missing nodes: %s", body)
				}
			}
			if !strings.Contains(body, "original") || !strings.Contains(body, "HK-VIP") {
				t.Fatalf("original or overridden remark missing: %s", body)
			}
		})
	}
}
