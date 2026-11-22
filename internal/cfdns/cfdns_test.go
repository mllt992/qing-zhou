package cfdns

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientZonesAndUpsert(t *testing.T) {
	records := map[string]Record{
		"rec-other": {ID: "rec-other", Type: "A", Name: "taken.mllt.vip", Content: "1.1.1.1", Proxied: false, Comment: "manual"},
		"rec-mine":  {ID: "rec-mine", Type: "A", Name: "mine.mllt.vip", Content: "2.2.2.2", Proxied: false, Comment: "managed-by:qingzhou;server:12"},
	}
	var lastProxied *bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-123" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"code": 10000, "message": "Authentication error"}}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": []Zone{
					{ID: "z1", Name: "mllt.vip", Status: "active"},
					{ID: "z2", Name: "sub.mllt.vip", Status: "active"},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/z1/dns_records":
			typ := r.URL.Query().Get("type")
			name := r.URL.Query().Get("name")
			var out []Record
			for _, rec := range records {
				if (typ == "" || rec.Type == typ) && (name == "" || rec.Name == name) {
					out = append(out, rec)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": out})
		case r.Method == http.MethodPost && r.URL.Path == "/zones/z1/dns_records":
			var body Record
			_ = json.NewDecoder(r.Body).Decode(&body)
			p := body.Proxied
			lastProxied = &p
			body.ID = "rec-new"
			records[body.ID] = body
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": body})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/zones/z1/dns_records/"):
			id := strings.TrimPrefix(r.URL.Path, "/zones/z1/dns_records/")
			var body Record
			_ = json.NewDecoder(r.Body).Decode(&body)
			p := body.Proxied
			lastProxied = &p
			body.ID = id
			records[id] = body
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": body})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/zones/z1/dns_records/"):
			id := strings.TrimPrefix(r.URL.Path, "/zones/z1/dns_records/")
			delete(records, id)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]string{"id": id}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := &Client{Token: "tok-123", APIBase: srv.URL, HTTP: srv.Client()}
	ctx := context.Background()

	// Longest zone match
	z, err := c.FindZoneForName(ctx, "node.sub.mllt.vip")
	if err != nil || z.ID != "z2" {
		t.Fatalf("FindZoneForName sub = %+v, err=%v", z, err)
	}
	z, err = c.FindZoneForName(ctx, "taxi.mllt.vip")
	if err != nil || z.ID != "z1" {
		t.Fatalf("FindZoneForName root = %+v, err=%v", z, err)
	}

	// Create new record (always grey cloud proxied=false)
	res, err := c.UpsertRecord(ctx, UpsertOptions{
		ZoneID: "z1", Type: "A", Name: "taxi.mllt.vip", Content: "217.216.188.164",
		ManagedTag: "managed-by:qingzhou;server:12",
	})
	if err != nil || res.Action != "created" || lastProxied == nil || *lastProxied {
		t.Fatalf("create failed: res=%+v err=%v proxied=%v", res, err, lastProxied)
	}

	// Idempotent second call -> unchanged
	res, err = c.UpsertRecord(ctx, UpsertOptions{
		ZoneID: "z1", Type: "A", Name: "taxi.mllt.vip", Content: "217.216.188.164",
		ManagedTag: "managed-by:qingzhou;server:12",
	})
	if err != nil || res.Action != "unchanged" {
		t.Fatalf("expected unchanged, got %+v err=%v", res, err)
	}

	// Conflict on manual record without Overwrite
	_, err = c.UpsertRecord(ctx, UpsertOptions{
		ZoneID: "z1", Type: "A", Name: "taken.mllt.vip", Content: "217.216.188.164",
		ManagedTag: "managed-by:qingzhou;server:12",
	})
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConflictError, got %v", err)
	}

	// Overwrite succeeds when explicitly confirmed
	res, err = c.UpsertRecord(ctx, UpsertOptions{
		ZoneID: "z1", Type: "A", Name: "taken.mllt.vip", Content: "217.216.188.164",
		ManagedTag: "managed-by:qingzhou;server:12", Overwrite: true,
	})
	if err != nil || res.Action != "updated" {
		t.Fatalf("overwrite failed: %+v err=%v", res, err)
	}

	// Managed record updates without Overwrite
	res, err = c.UpsertRecord(ctx, UpsertOptions{
		ZoneID: "z1", Type: "A", Name: "mine.mllt.vip", Content: "3.3.3.3",
		ManagedTag: "managed-by:qingzhou;server:12",
	})
	if err != nil || res.Action != "updated" || res.Record.Content != "3.3.3.3" {
		t.Fatalf("managed update failed: %+v err=%v", res, err)
	}

	// DeleteManagedRecords deletes only matching managedTag
	n, err := c.DeleteManagedRecords(ctx, "z1", "A", "mine.mllt.vip", "managed-by:qingzhou;server:99")
	if err != nil || n != 0 {
		t.Fatalf("wrong tag must not delete: n=%d err=%v", n, err)
	}
	n, err = c.DeleteManagedRecords(ctx, "z1", "A", "mine.mllt.vip", "managed-by:qingzhou;server:12")
	if err != nil || n != 1 {
		t.Fatalf("matching tag must delete: n=%d err=%v", n, err)
	}
}
