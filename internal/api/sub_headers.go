package api

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
)

func compressExceptSub(level int) func(http.Handler) http.Handler {
	compress := middleware.Compress(level)
	return func(next http.Handler) http.Handler {
		compressed := compress(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/sub/") {
				next.ServeHTTP(w, r)
				return
			}
			compressed.ServeHTTP(w, r)
		})
	}
}

const (
	settingSubCustomHeaders = "sub_custom_headers"
	maxSubCustomHeaders     = 32
	maxSubHeaderNameLen     = 128
	maxSubHeaderValueLen    = 4096
)

type subCustomHeader struct {
	Name  string
	Value string
}

// forbiddenSubHeaders lists transport/framing/security headers and panel-managed
// subscription headers that administrators must not override via custom headers.
var forbiddenSubHeaders = map[string]bool{
	// Framing / hop-by-hop / encoding headers
	"host":                true,
	"content-length":      true,
	"transfer-encoding":   true,
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"upgrade":             true,
	"content-encoding":    true,
	"vary":                true,
	// Security-sensitive response headers
	"set-cookie":                       true,
	"location":                         true,
	"www-authenticate":                 true,
	"strict-transport-security":        true,
	"access-control-allow-origin":      true,
	"access-control-allow-credentials": true,
	"content-security-policy":          true,
	"x-frame-options":                  true,
	"x-content-type-options":           true,
	// Panel-managed subscription metadata headers
	"subscription-userinfo": true,
	"content-type":          true,
	"cache-control":         true,
	"content-disposition":   true,
}

// parseSubCustomHeaders parses and strictly validates the multi-line
// sub_custom_headers setting. Blank lines and lines starting with '#' or '//'
// are ignored.
func parseSubCustomHeaders(raw string) ([]subCustomHeader, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	norm := strings.ReplaceAll(raw, "\r\n", "\n")
	norm = strings.ReplaceAll(norm, "\r", "\n")
	lines := strings.Split(norm, "\n")

	out := make([]subCustomHeader, 0, len(lines))
	seen := make(map[string]int)
	for i, rawLine := range lines {
		lineNum := i + 1
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		rawName, rawVal, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("第 %d 行缺少冒号（格式应为 Header-Name: value）", lineNum)
		}
		name := strings.TrimSpace(rawName)
		val := strings.TrimSpace(rawVal)
		if err := validateSubHeader(lineNum, name, val); err != nil {
			return nil, err
		}
		lower := strings.ToLower(name)
		if prev, dup := seen[lower]; dup {
			return nil, fmt.Errorf("第 %d 行响应头 %q 与第 %d 行重复", lineNum, name, prev)
		}
		seen[lower] = lineNum
		out = append(out, subCustomHeader{Name: name, Value: val})
		if len(out) > maxSubCustomHeaders {
			return nil, fmt.Errorf("自定义订阅响应头最多支持 %d 条", maxSubCustomHeaders)
		}
	}
	return out, nil
}

func validateSubHeader(lineNum int, name, val string) error {
	if name == "" {
		return fmt.Errorf("第 %d 行响应头名称不能为空", lineNum)
	}
	if len(name) > maxSubHeaderNameLen {
		return fmt.Errorf("第 %d 行响应头名称过长", lineNum)
	}
	for i := 0; i < len(name); i++ {
		if !isHTTPTokenChar(name[i]) {
			return fmt.Errorf("第 %d 行响应头名称 %q 含非法字符", lineNum, name)
		}
	}
	if forbiddenSubHeaders[strings.ToLower(name)] {
		return fmt.Errorf("第 %d 行禁止设置系统或危险响应头 %q", lineNum, name)
	}
	if val == "" {
		return fmt.Errorf("第 %d 行响应头 %q 的值不能为空", lineNum, name)
	}
	if len(val) > maxSubHeaderValueLen {
		return fmt.Errorf("第 %d 行响应头 %q 的值过长", lineNum, name)
	}
	for i := 0; i < len(val); i++ {
		b := val[i]
		if (b < 0x20 && b != '\t') || b == 0x7f {
			return fmt.Errorf("第 %d 行响应头 %q 的值含控制字符", lineNum, name)
		}
	}
	return nil
}

// isHTTPTokenChar implements RFC 9110 §5.6.2 tchar.
func isHTTPTokenChar(b byte) bool {
	if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') {
		return true
	}
	switch b {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}

// applySubCustomHeaders applies valid custom headers to dst while skipping any
// invalid or forbidden lines (defense-in-depth if the DB was edited directly).
func applySubCustomHeaders(dst http.Header, raw string) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	norm := strings.ReplaceAll(raw, "\r\n", "\n")
	norm = strings.ReplaceAll(norm, "\r", "\n")
	applied := 0
	for i, rawLine := range strings.Split(norm, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		rawName, rawVal, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name := strings.TrimSpace(rawName)
		val := strings.TrimSpace(rawVal)
		if validateSubHeader(i+1, name, val) != nil {
			continue
		}
		dst.Set(name, val)
		applied++
		if applied >= maxSubCustomHeaders {
			break
		}
	}
}

// acceptsGzip reports whether an HTTP Accept-Encoding header permits gzip
// (explicitly listing "gzip" with q > 0).
func acceptsGzip(acceptEncoding string) bool {
	if acceptEncoding == "" {
		return false
	}
	for _, part := range strings.Split(acceptEncoding, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		coding, params, _ := strings.Cut(part, ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			continue
		}
		q := 1.0
		for _, param := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(param), "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
				continue
			}
			parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				q = 0
			} else {
				q = parsed
			}
		}
		if q > 0 {
			return true
		}
	}
	return false
}

func compressGzip(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		_ = gw.Close()
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
