// Package cfdns is a minimal Cloudflare DNS v4 REST client used to manage
// per-server subdomain A/AAAA records (always DNS-only / grey cloud) using the
// panel's existing Cloudflare API Token (Zone:Read + Zone:DNS:Edit).
package cfdns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultAPIBase = "https://api.cloudflare.com/client/v4"

// Client talks to Cloudflare's v4 API with a scoped API token.
type Client struct {
	Token   string
	HTTP    *http.Client
	APIBase string // empty = defaultAPIBase; tests override
}

func (c *Client) httpc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (c *Client) base() string {
	if c != nil && strings.TrimSpace(c.APIBase) != "" {
		return strings.TrimRight(strings.TrimSpace(c.APIBase), "/")
	}
	return defaultAPIBase
}

// Zone is a Cloudflare DNS zone accessible by the API token.
type Zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Record is one DNS record in a Cloudflare zone.
type Record struct {
	ID      string `json:"id,omitempty"`
	ZoneID  string `json:"zone_id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment,omitempty"`
}

// ConflictError is returned when an existing DNS record for the requested name
// and type points elsewhere and Overwrite was not explicitly requested.
type ConflictError struct {
	Name     string
	Type     string
	Existing []Record
}

func (e *ConflictError) Error() string {
	var vals []string
	for _, r := range e.Existing {
		if r.Content != "" {
			vals = append(vals, r.Content)
		}
	}
	return fmt.Sprintf("域名 %s 已存在 %s 记录（指向 %s），为避免误覆盖请确认后再写入", e.Name, e.Type, strings.Join(vals, "、"))
}

// APIError is a Cloudflare API-level failure.
type APIError struct {
	StatusCode int
	Code       int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("Cloudflare API 错误（%d）: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("Cloudflare API 错误（HTTP %d）", e.StatusCode)
}

type cfEnvelope struct {
	Success bool            `json:"success"`
	Errors  []cfErrItem     `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cfErrItem struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) do(ctx context.Context, method, path string, payload any, out any) error {
	if c == nil || strings.TrimSpace(c.Token) == "" {
		return errors.New("未配置 Cloudflare API Token")
	}
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.Token))
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpc().Do(req)
	if err != nil {
		return fmt.Errorf("请求 Cloudflare 失败: %w", err)
	}
	defer resp.Body.Close()
	const maxBytes = 2 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return fmt.Errorf("读取 Cloudflare 响应失败: %w", err)
	}
	if len(raw) > maxBytes {
		return errors.New("Cloudflare 响应过大")
	}
	var env cfEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return &APIError{StatusCode: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	}
	if !env.Success || resp.StatusCode >= 400 {
		ae := &APIError{StatusCode: resp.StatusCode}
		if len(env.Errors) > 0 {
			ae.Code = env.Errors[0].Code
			ae.Message = env.Errors[0].Message
		}
		if resp.StatusCode == http.StatusUnauthorized {
			ae.Message = "Token 无效或已过期（" + ae.Message + "）"
		} else if resp.StatusCode == http.StatusForbidden {
			ae.Message = "Token 权限不足，请确认已授予「区域 · 区域 · 读取」和「区域 · DNS · 编辑」（" + ae.Message + "）"
		}
		return ae
	}
	if out == nil || len(env.Result) == 0 {
		return nil
	}
	return json.Unmarshal(env.Result, out)
}

// ListZones returns active zones accessible to this token.
func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	var zones []Zone
	if err := c.do(ctx, http.MethodGet, "/zones?status=active&per_page=50", nil, &zones); err != nil {
		return nil, err
	}
	if zones == nil {
		zones = []Zone{}
	}
	return zones, nil
}

// FindZoneForName finds the longest-matching active zone for a hostname
// (e.g. "taxi.mllt.vip" -> zone "mllt.vip").
func (c *Client) FindZoneForName(ctx context.Context, fqdn string) (*Zone, error) {
	fqdn = normalizeHost(fqdn)
	if fqdn == "" {
		return nil, errors.New("域名不能为空")
	}
	zones, err := c.ListZones(ctx)
	if err != nil {
		return nil, err
	}
	var best *Zone
	for i := range zones {
		z := &zones[i]
		zn := normalizeHost(z.Name)
		if zn == "" {
			continue
		}
		if fqdn == zn || strings.HasSuffix(fqdn, "."+zn) {
			if best == nil || len(zn) > len(normalizeHost(best.Name)) {
				best = z
			}
		}
	}
	if best == nil {
		return nil, fmt.Errorf("在 Cloudflare 账户中未找到匹配 %s 的根域名区域（请检查域名拼写与 Token 区域权限）", fqdn)
	}
	return best, nil
}

// ListRecords lists DNS records matching name (and optional type) inside zoneID.
func (c *Client) ListRecords(ctx context.Context, zoneID, recordType, name string) ([]Record, error) {
	q := url.Values{}
	if recordType != "" {
		q.Set("type", strings.ToUpper(strings.TrimSpace(recordType)))
	}
	if name != "" {
		q.Set("name", normalizeHost(name))
	}
	q.Set("per_page", "50")
	var recs []Record
	if err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/dns_records?"+q.Encode(), nil, &recs); err != nil {
		return nil, err
	}
	return recs, nil
}

// UpsertOptions controls UpsertRecord conflict and tagging behavior.
type UpsertOptions struct {
	ZoneID     string
	Type       string // "A" or "AAAA"
	Name       string // FQDN, e.g. "taxi.mllt.vip"
	Content    string // IPv4 or IPv6 literal
	ManagedTag string // e.g. "managed-by:qingzhou;server:12"
	Overwrite  bool
}

// UpsertResult describes what UpsertRecord did on Cloudflare.
type UpsertResult struct {
	Action string `json:"action"` // created | updated | unchanged
	Record Record `json:"record"`
}

// UpsertRecord ensures a DNS-only (proxied=false) A/AAAA record exists for
// opt.Name. If a record already exists with different content and is neither
// managed by the same ManagedTag nor allowed by Overwrite, ConflictError is
// returned without modifying Cloudflare.
func (c *Client) UpsertRecord(ctx context.Context, opt UpsertOptions) (*UpsertResult, error) {
	recType := strings.ToUpper(strings.TrimSpace(opt.Type))
	name := normalizeHost(opt.Name)
	content := strings.TrimSpace(opt.Content)
	if opt.ZoneID == "" || name == "" || content == "" || (recType != "A" && recType != "AAAA") {
		return nil, errors.New("DNS 记录参数无效")
	}

	existing, err := c.ListRecords(ctx, opt.ZoneID, recType, name)
	if err != nil {
		return nil, err
	}
	// Check CNAME conflict as well — Cloudflare rejects A/AAAA alongside a CNAME.
	cnames, err := c.ListRecords(ctx, opt.ZoneID, "CNAME", name)
	if err == nil && len(cnames) > 0 && !opt.Overwrite {
		return nil, &ConflictError{Name: name, Type: "CNAME", Existing: cnames}
	}

	payload := map[string]any{
		"type":    recType,
		"name":    name,
		"content": content,
		"ttl":     1,     // Auto
		"proxied": false, // Always grey cloud (DNS only) for proxy nodes
		"comment": opt.ManagedTag,
	}

	if len(existing) == 0 {
		// Remove conflicting CNAME when overwrite was explicitly confirmed.
		for _, cn := range cnames {
			if cn.ID != "" {
				_ = c.DeleteRecord(ctx, opt.ZoneID, cn.ID)
			}
		}
		var created Record
		if err := c.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(opt.ZoneID)+"/dns_records", payload, &created); err != nil {
			return nil, err
		}
		return &UpsertResult{Action: "created", Record: created}, nil
	}

	// Single existing record matching exact target & already DNS-only -> idempotent.
	if len(existing) == 1 && strings.EqualFold(existing[0].Content, content) && !existing[0].Proxied {
		if existing[0].Comment == opt.ManagedTag || opt.ManagedTag == "" {
			return &UpsertResult{Action: "unchanged", Record: existing[0]}, nil
		}
	}

	// Find whether all existing records belong to this managed server.
	allManaged := opt.ManagedTag != ""
	for _, r := range existing {
		if r.Comment != opt.ManagedTag {
			allManaged = false
			break
		}
	}
	if !allManaged && !opt.Overwrite {
		// Allow idempotent adopt when content matches and proxied is false.
		if !(len(existing) == 1 && strings.EqualFold(existing[0].Content, content) && !existing[0].Proxied) {
			return nil, &ConflictError{Name: name, Type: recType, Existing: existing}
		}
	}

	target := existing[0]
	var updated Record
	if err := c.do(ctx, http.MethodPut, "/zones/"+url.PathEscape(opt.ZoneID)+"/dns_records/"+url.PathEscape(target.ID), payload, &updated); err != nil {
		return nil, err
	}
	// Clean any extra duplicate records of the same type if we own/overwrite them.
	for _, dup := range existing[1:] {
		if dup.ID != "" && (opt.Overwrite || (opt.ManagedTag != "" && dup.Comment == opt.ManagedTag)) {
			_ = c.DeleteRecord(ctx, opt.ZoneID, dup.ID)
		}
	}
	return &UpsertResult{Action: "updated", Record: updated}, nil
}

// DeleteRecord removes one DNS record by ID.
func (c *Client) DeleteRecord(ctx context.Context, zoneID, recordID string) error {
	return c.do(ctx, http.MethodDelete, "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(recordID), nil, nil)
}

// DeleteManagedRecords deletes only records for name (and optional type) whose
// comment exactly matches managedTag. Returns how many records were deleted.
func (c *Client) DeleteManagedRecords(ctx context.Context, zoneID, recordType, name, managedTag string) (int, error) {
	if managedTag == "" || zoneID == "" || normalizeHost(name) == "" {
		return 0, nil
	}
	recs, err := c.ListRecords(ctx, zoneID, recordType, name)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, r := range recs {
		if r.ID == "" || r.Comment != managedTag {
			continue
		}
		if err := c.DeleteRecord(ctx, zoneID, r.ID); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

func normalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}
