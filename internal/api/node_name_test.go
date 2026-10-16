package api

import (
	"testing"

	"qingzhou/internal/store"
)

func TestRequireNodeName(t *testing.T) {
	for _, name := range []string{"", "   ", "\t\n"} {
		n := store.Node{Name: name}
		if msg := requireNodeName(&n); msg == "" {
			t.Fatalf("name %q accepted, want rejection", name)
		}
	}
	n := store.Node{Name: "  香港 01  "}
	if msg := requireNodeName(&n); msg != "" {
		t.Fatalf("valid name rejected: %s", msg)
	}
	if n.Name != "香港 01" {
		t.Fatalf("name not trimmed: %q", n.Name)
	}
}
