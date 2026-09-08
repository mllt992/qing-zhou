package subconv

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestWithLinkRemarkPreservesConnection(t *testing.T) {
	vmess := "vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"v":"2","ps":"old","add":"example.com","port":"443","id":"11111111-1111-1111-1111-111111111111","aid":"0","net":"tcp","tls":"tls","custom":{"keep":true}}`))
	for _, link := range []string{
		"trojan://secret@example.com:443?sni=example.com#old",
		"hysteria2://secret@example.com:443?obfs=salamander&obfs-password=pw#old",
		vmess,
	} {
		t.Run(strings.Split(link, ":")[0], func(t *testing.T) {
			if got := WithLinkRemark(link, "  "); got != link {
				t.Fatal("empty remark changed legacy link")
			}
			remark := "香港 #1 + VIP"
			got := WithLinkRemark(link, remark)
			if strings.HasPrefix(link, "vmess://") {
				decoded, err := b64decode(strings.TrimPrefix(got, "vmess://"))
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(decoded, &fields); err != nil {
					t.Fatal(err)
				}
				var name string
				_ = json.Unmarshal(fields["ps"], &name)
				if name != remark || string(fields["custom"]) != `{"keep":true}` {
					t.Fatalf("VMess remark or unknown fields lost: %s", decoded)
				}
			} else {
				base, _, _ := strings.Cut(link, "#")
				if got != base+"#"+url.QueryEscape(remark) {
					t.Fatalf("unexpected link: %s", got)
				}
			}
			before, after := ParseLinks([]string{link}), ParseLinks([]string{got})
			if len(before) != 1 || len(after) != 1 {
				t.Fatal("link no longer parses")
			}
			if before[0].Server != after[0].Server || before[0].Port != after[0].Port || before[0].Password != after[0].Password || before[0].UUID != after[0].UUID {
				t.Fatal("connection credentials changed")
			}
		})
	}
}
