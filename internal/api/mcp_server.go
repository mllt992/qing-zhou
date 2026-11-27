package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"qingzhou/internal/store"
	"qingzhou/internal/version"
)

const ctxMCPAuth ctxKey = "mcp_auth"

type mcpAuthInfo struct {
	TokenID   int64
	TokenName string
	UserID    int64
	Scopes    []string
	ClientIP  string
}

func mcpAuthFromContext(ctx context.Context) (mcpAuthInfo, bool) {
	v, ok := ctx.Value(ctxMCPAuth).(mcpAuthInfo)
	return v, ok
}

// mcpHTTPHandler returns the authenticated Remote MCP (Streamable HTTP) handler
// mounted at /mcp. Only valid, active admin API tokens carrying at least one
// mcp:* scope are accepted.
func (a *API) mcpHTTPHandler() http.Handler {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "qingzhou-panel",
		Version: version.Current(),
	}, &mcp.ServerOptions{
		Instructions: "轻舟面板（QingZhou）内置远程 MCP 运维服务。排查节点/订阅/流量问题优先调用 diagnose_panel；新手新建可用节点优先调用 quick_setup_node；涉及删除、下架或凭据重置的工具默认 confirm=false 仅返回影响预览，请向用户确认后再传 confirm=true 执行。",
	})

	a.registerMCPReadTools(srv)
	a.registerMCPInfraTools(srv)
	a.registerMCPUserTools(srv)

	streamable := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server {
		return srv
	}, &mcp.StreamableHTTPOptions{
		Stateless:                  true,
		JSONResponse:               true,
		DisableLocalhostProtection: true, // Protected by mandatory Bearer API token; safe behind 127.0.0.1 reverse proxies.
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if a.mcpRL != nil && !a.mcpRL.allow(ip) {
			fail(w, http.StatusTooManyRequests, "MCP 请求过于频繁，请稍后再试")
			return
		}
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			fail(w, http.StatusUnauthorized, "缺少有效的 Bearer MCP Token")
			return
		}
		plain := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		at, err := a.st.LookupAPIToken(plain)
		if err != nil || at == nil {
			fail(w, http.StatusUnauthorized, "MCP Token 无效、已过期或已吊销")
			return
		}
		owner, err := a.st.UserByID(at.CreatedBy)
		if err != nil || owner == nil || owner.Status != "active" || owner.Role != "admin" {
			fail(w, http.StatusUnauthorized, "MCP Token 所属管理员账号不可用")
			return
		}
		if !store.HasAnyMCPScope(at.Scopes) {
			fail(w, http.StatusForbidden, "该 Token 未授予任何 MCP 权限（需 mcp:* scope）")
			return
		}
		a.st.TouchAPIToken(at.ID)
		ctx := context.WithValue(r.Context(), ctxMCPAuth, mcpAuthInfo{
			TokenID:   at.ID,
			TokenName: at.Name,
			UserID:    at.CreatedBy,
			Scopes:    at.Scopes,
			ClientIP:  ip,
		})
		streamable.ServeHTTP(w, r.WithContext(ctx))
	})
}

// addMCPTool registers a typed MCP tool guarded by requiredScope and wrapped
// with automatic audit logging and sanitized argument capture.
func addMCPTool[In any](a *API, srv *mcp.Server, tool *mcp.Tool, requiredScope string, fn func(ctx context.Context, auth mcpAuthInfo, in In) (any, error)) {
	mcp.AddTool[In, any](srv, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		start := time.Now()
		auth, ok := mcpAuthFromContext(ctx)
		if !ok {
			return nil, nil, errors.New("未认证的 MCP 请求")
		}
		summary := summarizeMCPArgs(in)
		record := func(status, errMsg string) {
			if a.st == nil {
				return
			}
			_ = a.st.InsertMCPAuditLog(store.MCPAuditLog{
				TokenID:     auth.TokenID,
				TokenName:   auth.TokenName,
				ToolName:    tool.Name,
				ArgsSummary: summary,
				Status:      status,
				ErrorMsg:    truncateRunesAPI(errMsg, 300),
				DurationMS:  time.Since(start).Milliseconds(),
				ClientIP:    auth.ClientIP,
			})
		}
		if requiredScope != "" && !store.APITokenHasScope(auth.Scopes, requiredScope) {
			msg := fmt.Sprintf("权限不足：调用 %s 需要 %s scope", tool.Name, requiredScope)
			record("denied", msg)
			return nil, nil, errors.New(msg)
		}
		out, err := fn(ctx, auth, in)
		if err != nil {
			record("error", err.Error())
			return nil, nil, err
		}
		record("ok", "")
		return mcpJSONResult(out)
	})
}

func mcpJSONResult(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("序列化结果失败: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(b)},
		},
	}, nil, nil
}

// mcpSecretFieldNames lists JSON field substrings that must be scrubbed from
// MCP audit log argument summaries so secrets never land in plaintext logs.
// "pass" covers smtp_pass / ssh_key_pass, which do not contain the longer
// "password" token. "oauth" covers oauth2_config. upstream_* and
// backup_s3_config are credential documents handled in isMCPSecretKey.
var mcpSecretFieldNames = []string{
	"password", "passwd", "pass", "secret", "private_key", "ssh_key",
	"bot_token", "token", "api_key", "key_pem", "certificate", "key",
	"oauth",
}

func isMCPSecretKey(k string) bool {
	lk := strings.ToLower(strings.TrimSpace(k))
	switch lk {
	case "queue_key", "key_type", "public_key", "ssh_key_path", "confirm":
		return false
	}
	if strings.HasPrefix(lk, "upstream_") || lk == "backup_s3_config" {
		return true
	}
	for _, sub := range mcpSecretFieldNames {
		if strings.Contains(lk, sub) {
			return true
		}
	}
	return false
}

func scrubMCPValue(v any) any {
	switch tv := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(tv))
		for k, val := range tv {
			if isMCPSecretKey(k) {
				if s, ok := val.(string); ok && s != "" {
					out[k] = "***"
				} else if val != nil {
					out[k] = "***"
				}
				continue
			}
			out[k] = scrubMCPValue(val)
		}
		return out
	case []any:
		out := make([]any, len(tv))
		for i, item := range tv {
			out[i] = scrubMCPValue(item)
		}
		return out
	default:
		return v
	}
}

func summarizeMCPArgs(in any) string {
	b, err := json.Marshal(in)
	if err != nil || len(b) == 0 || string(b) == "{}" || string(b) == "null" {
		return ""
	}
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return ""
	}
	clean, err := json.Marshal(scrubMCPValue(raw))
	if err != nil {
		return ""
	}
	return truncateRunesAPI(string(clean), 400)
}

// GET /api/admin/mcp/audit-logs — recent MCP tool calls (admin JWT or mcp:read).
func (a *API) handleAdminMCPAuditLogs(w http.ResponseWriter, r *http.Request) {
	limit := int(atoi(r.URL.Query().Get("limit")))
	if limit <= 0 {
		limit = 50
	}
	offset := int(atoi(r.URL.Query().Get("offset")))
	tool := strings.TrimSpace(r.URL.Query().Get("tool"))
	rows, total, err := a.st.ListMCPAuditLogs(limit, offset, tool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取 MCP 审计日志失败")
		return
	}
	ok(w, J{"rows": rows, "total": total})
}
