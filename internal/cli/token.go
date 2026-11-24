package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"qingzhou/internal/api"
	"qingzhou/internal/config"
	"qingzhou/internal/store"
)

// Run dispatches local CLI subcommands (currently `token`).
// Returns an exit code (0 on success).
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "token":
		return runToken(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "未知子命令: %s\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `轻舟面板 CLI：
  qingzhou                      直接启动面板服务（默认）
  qingzhou token create [flags] 在本机签发 API / MCP 令牌
  qingzhou token list [flags]   列出已有 API / MCP 令牌
  qingzhou token revoke <id>    吊销指定 ID 的令牌`)
}

func runToken(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printTokenUsage(stderr)
		return 2
	}
	switch args[0] {
	case "create":
		return runTokenCreate(args[1:], stdout, stderr)
	case "list":
		return runTokenList(args[1:], stdout, stderr)
	case "revoke":
		return runTokenRevoke(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		printTokenUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "未知 token 子命令: %s\n\n", args[0])
		printTokenUsage(stderr)
		return 2
	}
}

func printTokenUsage(w io.Writer) {
	fmt.Fprintln(w, `用法：
  qingzhou token create [--name ai-ops] [--preset mcp-admin|mcp-ops|mcp-readonly] [--scopes s1,s2] [--expires-days 0] [--panel-url https://...] [--env-file /opt/qingzhou/qingzhou.env] [--json]
  qingzhou token list [--env-file /opt/qingzhou/qingzhou.env] [--json]
  qingzhou token revoke <id> [--env-file /opt/qingzhou/qingzhou.env]`)
}

func openLocalStore(envFile string) (*store.Store, *config.Config, error) {
	loadEnvDefaults(envFile)
	cfg := config.Load()
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, nil, fmt.Errorf("打开数据库 %s 失败: %w", cfg.DBPath, err)
	}
	if key := os.Getenv("QZ_SECRET_KEY"); key != "" {
		st.SetSecretKey([]byte(key))
	}
	if err := st.Migrate(); err != nil {
		st.Close()
		return nil, nil, fmt.Errorf("数据库迁移失败: %w", err)
	}
	if _, err := st.Seed(cfg); err != nil {
		st.Close()
		return nil, nil, fmt.Errorf("初始化管理员失败: %w", err)
	}
	return st, cfg, nil
}

// loadEnvDefaults reads KEY=VALUE lines from an env file without overwriting
// variables already exported in the caller's process environment.
func loadEnvDefaults(explicitPath string) {
	candidates := []string{}
	if strings.TrimSpace(explicitPath) != "" {
		candidates = append(candidates, explicitPath)
	} else if os.Getenv("QZ_DB") == "" {
		if exe, err := os.Executable(); err == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(exe), "qingzhou.env"))
		}
		candidates = append(candidates, "/opt/qingzhou/qingzhou.env", "qingzhou.env")
	}
	for _, path := range candidates {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			v = strings.Trim(v, `"'`)
			if k != "" && os.Getenv(k) == "" {
				_ = os.Setenv(k, v)
			}
		}
		_ = f.Close()
		return
	}
}

func resolvePanelBase(st *store.Store, cfg *config.Config, override string) string {
	for _, cand := range []string{
		strings.TrimSpace(override),
		strings.TrimSpace(os.Getenv("QZ_PUBLIC_BASE")),
	} {
		if cand != "" {
			return normalizeBaseCLI(cand)
		}
	}
	if v, _ := st.GetSetting("public_base"); strings.TrimSpace(v) != "" {
		return normalizeBaseCLI(v)
	}
	listen := cfg.ListenAddr
	if strings.HasPrefix(listen, ":") {
		listen = "127.0.0.1" + listen
	}
	listen = strings.Replace(listen, "0.0.0.0", "127.0.0.1", 1)
	return "http://" + listen
}

func normalizeBaseCLI(v string) string {
	v = strings.TrimRight(strings.TrimSpace(v), "/")
	if v == "" {
		return ""
	}
	if !strings.Contains(v, "://") {
		v = "https://" + v
	}
	return v
}

func runTokenCreate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "ai-ops", "令牌名称")
	preset := fs.String("preset", "mcp-admin", "权限预设（mcp-admin | mcp-ops | mcp-readonly）")
	scopesRaw := fs.String("scopes", "", "逗号分隔的自定义 scope（指定后覆盖 preset）")
	expiresDays := fs.Int64("expires-days", 0, "有效天数（0 = 永不过期）")
	panelURL := fs.String("panel-url", "", "面板对外访问地址（用于拼接 mcp_url）")
	envFile := fs.String("env-file", "", "可选 qingzhou.env 路径")
	asJSON := fs.Bool("json", false, "以 JSON 输出")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var scopes []string
	if strings.TrimSpace(*scopesRaw) != "" {
		for _, part := range strings.Split(*scopesRaw, ",") {
			if s := strings.TrimSpace(part); s != "" {
				scopes = append(scopes, s)
			}
		}
	} else {
		resolved, ok := store.ResolveAPITokenPreset(*preset)
		if !ok {
			fmt.Fprintf(stderr, "错误：未知 preset %q（可选：mcp-admin / mcp-ops / mcp-readonly）\n", *preset)
			return 1
		}
		scopes = resolved
	}

	st, cfg, err := openLocalStore(*envFile)
	if err != nil {
		fmt.Fprintf(stderr, "错误：%v\n", err)
		return 1
	}
	defer st.Close()

	adminID, err := st.FirstActiveAdminID()
	if err != nil {
		fmt.Fprintf(stderr, "错误：未找到可用的管理员账号：%v\n", err)
		return 1
	}

	var expiresAt int64
	if *expiresDays > 0 {
		expiresAt = time.Now().Unix() + (*expiresDays)*86400
	}

	plain, tok, err := st.CreateAPIToken(*name, scopes, adminID, expiresAt)
	if err != nil {
		fmt.Fprintf(stderr, "错误：创建令牌失败：%v\n", err)
		return 1
	}

	panelBase := resolvePanelBase(st, cfg, *panelURL)
	mcpURL := strings.TrimRight(panelBase, "/") + "/mcp"
	mcpCfg := api.BuildMCPClientConfig(mcpURL, plain)

	payload := map[string]any{
		"id":         tok.ID,
		"name":       tok.Name,
		"prefix":     tok.Prefix,
		"scopes":     tok.Scopes,
		"expires_at": tok.ExpiresAt,
		"created_at": tok.CreatedAt,
		"token":      plain,
		"panel_url":  panelBase,
	}
	if store.HasAnyMCPScope(tok.Scopes) {
		payload["mcp_url"] = mcpURL
		payload["mcp_config"] = mcpCfg
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(payload)
		return 0
	}

	fmt.Fprintf(stdout, "✅ 已创建令牌 #%d (%s)\n", tok.ID, tok.Name)
	fmt.Fprintf(stdout, "   Scopes:  %s\n", strings.Join(tok.Scopes, ", "))
	fmt.Fprintf(stdout, "   Token:   %s\n", plain)
	fmt.Fprintf(stdout, "   ⚠️  明文 Token 仅展示这一次，请立即保存。\n")
	if store.HasAnyMCPScope(tok.Scopes) {
		b, _ := json.MarshalIndent(mcpCfg, "", "  ")
		fmt.Fprintf(stdout, "\n   Remote MCP 端点: %s\n", mcpURL)
		fmt.Fprintf(stdout, "   可直接粘贴到 AI 客户端的 MCP 配置：\n%s\n", string(b))
	}
	return 0
}

func runTokenList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("token list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	envFile := fs.String("env-file", "", "可选 qingzhou.env 路径")
	asJSON := fs.Bool("json", false, "以 JSON 输出")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	st, _, err := openLocalStore(*envFile)
	if err != nil {
		fmt.Fprintf(stderr, "错误：%v\n", err)
		return 1
	}
	defer st.Close()

	list, err := st.ListAPITokens()
	if err != nil {
		fmt.Fprintf(stderr, "错误：读取令牌失败：%v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(list)
		return 0
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "暂无令牌")
		return 0
	}
	now := time.Now().Unix()
	for _, t := range list {
		status := "active"
		if t.RevokedAt > 0 {
			status = "revoked"
		} else if t.ExpiresAt > 0 && t.ExpiresAt <= now {
			status = "expired"
		}
		fmt.Fprintf(stdout, "#%d\t%s\t%s...\t[%s]\t%s\n", t.ID, t.Name, t.Prefix, status, strings.Join(t.Scopes, ","))
	}
	return 0
}

func runTokenRevoke(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("token revoke", flag.ContinueOnError)
	fs.SetOutput(stderr)
	envFile := fs.String("env-file", "", "可选 qingzhou.env 路径")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "错误：请提供要吊销的令牌 ID，例如：qingzhou token revoke 1")
		return 2
	}
	id, err := strconv.ParseInt(strings.TrimSpace(fs.Arg(0)), 10, 64)
	if err != nil || id <= 0 {
		fmt.Fprintf(stderr, "错误：无效令牌 ID: %q\n", fs.Arg(0))
		return 2
	}

	st, _, err := openLocalStore(*envFile)
	if err != nil {
		fmt.Fprintf(stderr, "错误：%v\n", err)
		return 1
	}
	defer st.Close()

	if err := st.RevokeAPIToken(id); err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			fmt.Fprintf(stderr, "错误：令牌 #%d 不存在\n", id)
		} else {
			fmt.Fprintf(stderr, "错误：吊销失败：%v\n", err)
		}
		return 1
	}
	fmt.Fprintf(stdout, "✅ 已吊销令牌 #%d\n", id)
	return 0
}
