package store

import (
	"time"
)

const mcpAuditSchema = `
CREATE TABLE IF NOT EXISTS mcp_audit_logs (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  token_id     INTEGER NOT NULL DEFAULT 0,
  token_name   TEXT    NOT NULL DEFAULT '',
  tool_name    TEXT    NOT NULL,
  args_summary TEXT    NOT NULL DEFAULT '',
  status       TEXT    NOT NULL DEFAULT 'ok',
  error_msg    TEXT    NOT NULL DEFAULT '',
  duration_ms  INTEGER NOT NULL DEFAULT 0,
  client_ip    TEXT    NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mcp_audit_logs_created ON mcp_audit_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mcp_audit_logs_token ON mcp_audit_logs(token_id, created_at DESC);
`

// MCPAuditLog is one recorded MCP tool invocation.
type MCPAuditLog struct {
	ID          int64  `json:"id"`
	TokenID     int64  `json:"token_id"`
	TokenName   string `json:"token_name"`
	ToolName    string `json:"tool_name"`
	ArgsSummary string `json:"args_summary"`
	Status      string `json:"status"`
	ErrorMsg    string `json:"error_msg"`
	DurationMS  int64  `json:"duration_ms"`
	ClientIP    string `json:"client_ip"`
	CreatedAt   int64  `json:"created_at"`
}

// InsertMCPAuditLog persists one MCP tool invocation and keeps table growth
// bounded (retains the most recent 5000 rows via periodic lightweight pruning).
func (s *Store) InsertMCPAuditLog(entry MCPAuditLog) error {
	if entry.CreatedAt == 0 {
		entry.CreatedAt = time.Now().Unix()
	}
	if entry.Status == "" {
		entry.Status = "ok"
	}
	_, err := s.db.Exec(`INSERT INTO mcp_audit_logs
		(token_id, token_name, tool_name, args_summary, status, error_msg, duration_ms, client_ip, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		entry.TokenID, entry.TokenName, entry.ToolName, entry.ArgsSummary,
		entry.Status, entry.ErrorMsg, entry.DurationMS, entry.ClientIP, entry.CreatedAt)
	if err != nil {
		return err
	}
	// Best-effort cap to keep SQLite small on busy AI-ops panels.
	_, _ = s.db.Exec(`DELETE FROM mcp_audit_logs WHERE id < (SELECT COALESCE(MAX(id), 0) - 5000 FROM mcp_audit_logs)`)
	return nil
}

// ListMCPAuditLogs returns recent MCP tool invocations newest first.
func (s *Store) ListMCPAuditLogs(limit, offset int, toolName string) ([]*MCPAuditLog, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	var err error
	if toolName != "" {
		err = s.db.QueryRow(`SELECT COUNT(*) FROM mcp_audit_logs WHERE tool_name=?`, toolName).Scan(&total)
	} else {
		err = s.db.QueryRow(`SELECT COUNT(*) FROM mcp_audit_logs`).Scan(&total)
	}
	if err != nil {
		return nil, 0, err
	}
	var rowsErr error
	var rowsQuery string
	var args []any
	if toolName != "" {
		rowsQuery = `SELECT id, token_id, token_name, tool_name, args_summary, status, error_msg, duration_ms, client_ip, created_at
			FROM mcp_audit_logs WHERE tool_name=? ORDER BY id DESC LIMIT ? OFFSET ?`
		args = []any{toolName, limit, offset}
	} else {
		rowsQuery = `SELECT id, token_id, token_name, tool_name, args_summary, status, error_msg, duration_ms, client_ip, created_at
			FROM mcp_audit_logs ORDER BY id DESC LIMIT ? OFFSET ?`
		args = []any{limit, offset}
	}
	rows, err := s.db.Query(rowsQuery, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*MCPAuditLog{}
	for rows.Next() {
		var l MCPAuditLog
		if err := rows.Scan(&l.ID, &l.TokenID, &l.TokenName, &l.ToolName, &l.ArgsSummary,
			&l.Status, &l.ErrorMsg, &l.DurationMS, &l.ClientIP, &l.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, &l)
	}
	rowsErr = rows.Err()
	return out, total, rowsErr
}
