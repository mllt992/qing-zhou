// Package updater implements in-panel self-update against GitHub Releases.
//
// It queries the repo's latest release, compares the tag against the running
// binary's version (see internal/version), and — on admin request — downloads
// the matching Linux asset, verifies its sha256 against the digest GitHub
// publishes for the asset, atomically replaces the running binary, and re-execs
// the process so the new version takes over (same PID; works with systemd's
// Restart=on-failure without needing the unit name or extra privileges).
//
// The binary swap + re-exec is Linux-only (see restart_linux.go); on other
// platforms Apply refuses with a clear message. Checking for updates works
// everywhere.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"qingzhou/internal/version"
)

// DefaultRepo is the GitHub "owner/name" polled when no override is configured.
const DefaultRepo = "mllt992/qing-zhou"

// assetName is the release asset this binary knows how to install for the
// current OS/arch, e.g. "qingzhou-linux-amd64".
func assetName() string {
	return fmt.Sprintf("qingzhou-%s-%s", runtime.GOOS, runtime.GOARCH)
}

func probeAssetName(arch string) string {
	return "probe-linux-" + arch
}

// Status is a coarse phase of an in-flight update.
type Status string

const (
	StatusIdle         Status = "idle"
	StatusDownloading  Status = "downloading"
	StatusVerifying    Status = "verifying"
	StatusSnapshotting Status = "snapshotting"
	StatusInstalling   Status = "installing"
	StatusRestarting   Status = "restarting"
	StatusFailed       Status = "failed"
)

// State is the observable progress of the updater, returned by the status API.
type State struct {
	Status        Status `json:"status"`
	Message       string `json:"message"`
	Percent       int    `json:"percent"`
	TargetVersion string `json:"target_version"`
	StartedAt     int64  `json:"started_at,omitempty"`
}

// CheckResult is what the check API returns to the admin UI.
type CheckResult struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	Name            string `json:"name"`
	Notes           string `json:"notes"`
	URL             string `json:"url"`
	PublishedAt     string `json:"published_at"`
	UpdateAvailable bool   `json:"update_available"`
	Downloadable    bool   `json:"downloadable"`
	AssetName       string `json:"asset_name"`
	AssetSize       int64  `json:"asset_size"`
	Dev             bool   `json:"dev"`
}

// ReleaseInfo is one selectable release in the version picker.
type ReleaseInfo struct {
	Tag         string `json:"tag"`
	Name        string `json:"name"`
	PublishedAt string `json:"published_at"`
	Prerelease  bool   `json:"prerelease"`
	// Downloadable is false when the release predates this architecture's asset
	// (or the upload failed). Such a release is listed but cannot be installed —
	// hiding it would leave an unexplained gap in the version history.
	Downloadable bool  `json:"downloadable"`
	AssetSize    int64 `json:"asset_size"`
	// Relation to the running build: "current", "newer" or "older".
	Relation string `json:"relation"`
}

// Manager coordinates update checks and the (single-flight) apply operation.
type Manager struct {
	repoFn  func() string // resolves the configured "owner/name" (env/setting/default)
	tokenFn func() string // optional GitHub token to lift the 60/hr anon rate limit
	client  *http.Client

	mu             sync.Mutex
	state          State
	running        bool
	probeMu        sync.Mutex
	snapshotMu     sync.Mutex
	snapshotSource SnapshotSource
}

// New builds a Manager. repoFn/tokenFn may be nil; sensible defaults are used.
// Supply a SnapshotSource to permit binary replacement; without one, checks
// still work but installation and rollback fail closed.
func New(repoFn, tokenFn func() string, source ...SnapshotSource) *Manager {
	if repoFn == nil {
		repoFn = func() string { return DefaultRepo }
	}
	if tokenFn == nil {
		tokenFn = func() string { return "" }
	}
	var snapshots SnapshotSource
	if len(source) > 0 {
		snapshots = source[0]
	}
	return &Manager{
		snapshotSource: snapshots,
		repoFn:         repoFn,
		tokenFn:        tokenFn,
		// No overall client timeout: a release binary is tens of MB and the
		// download is bounded by the caller's context instead.
		client: &http.Client{},
		state:  State{Status: StatusIdle},
	}
}

func (m *Manager) repo() string {
	r := strings.TrimSpace(m.repoFn())
	if r == "" {
		return DefaultRepo
	}
	return r
}

// ghRelease mirrors the subset of the GitHub releases API we consume.
type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt string    `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"` // e.g. "sha256:abcd..."
}

func (m *Manager) newRequest(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "qingzhou-updater")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if tok := strings.TrimSpace(m.tokenFn()); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return req, nil
}

// releaseHTTPError turns a non-200 releases response into the message the admin
// sees. Shared by every releases call so they fail in the same words.
func releaseHTTPError(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return errors.New("未找到该发布版本（仓库无 release、版本号有误，或名称配置有误）")
	case http.StatusForbidden, http.StatusTooManyRequests:
		// Name where the token is configured, not just its setting key: the key
		// was only reachable by hand-editing the DB until it got a field, and an
		// error that names a key with no visible home reads as a dead end.
		return errors.New("GitHub API 速率受限，请稍后再试（或在「系统设置 → 在线更新」填一个 GitHub Token 提高额度）")
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GitHub 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// getRelease fetches and decodes a single release document.
func (m *Manager) getRelease(ctx context.Context, url string) (*ghRelease, error) {
	req, err := m.newRequest(ctx, url)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 GitHub 失败: %w", err)
	}
	defer resp.Body.Close()
	if err := releaseHTTPError(resp); err != nil {
		return nil, err
	}
	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("解析 GitHub 响应失败: %w", err)
	}
	return &rel, nil
}

// latestRelease fetches the newest non-draft release for the configured repo.
func (m *Manager) latestRelease(ctx context.Context) (*ghRelease, error) {
	return m.getRelease(ctx, fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", m.repo()))
}

// releaseByTag fetches one specific release. Used when the admin pinned a
// version rather than taking whatever /releases/latest currently points at.
func (m *Manager) releaseByTag(ctx context.Context, tag string) (*ghRelease, error) {
	// The tag goes into a URL path. Anything that could escape the path segment
	// is refused up front rather than URL-escaped, because a tag containing a
	// slash or a traversal sequence is not a tag we published.
	if !validTag(tag) {
		return nil, fmt.Errorf("版本号格式非法: %q", tag)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", m.repo(), tag)
	rel, err := m.getRelease(ctx, url)
	if err != nil {
		return nil, err
	}
	// GitHub resolved the tag, but bind the answer to what was asked for: the
	// whole point of pinning is that the installed bytes belong to the version
	// the admin chose.
	if rel.TagName != tag {
		return nil, fmt.Errorf("GitHub 返回的版本 %q 与请求的 %q 不一致，已取消", rel.TagName, tag)
	}
	return rel, nil
}

// validTag accepts the character set real release tags use. Deliberately
// restrictive: this value is interpolated into a URL path.
func validTag(tag string) bool {
	if tag == "" || len(tag) > 64 {
		return false
	}
	for _, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == '+':
		default:
			return false
		}
	}
	return true
}

// ListReleases returns the most recent releases, newest first, for the version
// picker. Drafts are omitted (they have no public assets); pre-releases are
// kept but flagged, so an operator can deliberately install one.
func (m *Manager) ListReleases(ctx context.Context, limit int) ([]ReleaseInfo, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=%d", m.repo(), limit)
	req, err := m.newRequest(ctx, url)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 GitHub 失败: %w", err)
	}
	defer resp.Body.Close()
	if err := releaseHTTPError(resp); err != nil {
		return nil, err
	}
	var rels []ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&rels); err != nil {
		return nil, fmt.Errorf("解析 GitHub 响应失败: %w", err)
	}

	cur, dev := version.Current(), version.IsDev()
	out := make([]ReleaseInfo, 0, len(rels))
	for i := range rels {
		rel := &rels[i]
		if rel.Draft {
			continue
		}
		// Deliberately without the release body: the picker shows a one-line
		// label, and 30 changelogs — several KB each here — would be shipped to
		// the browser on every list load for nothing. The GitHub link covers it.
		info := ReleaseInfo{
			Tag: rel.TagName, Name: rel.Name, PublishedAt: rel.PublishedAt,
			Prerelease: rel.Prerelease,
		}
		if a := findAsset(rel, assetName()); a != nil {
			info.Downloadable = true
			info.AssetSize = a.Size
		}
		switch {
		case dev:
			// A dev build has no comparable version, so no ordering claim is made.
			info.Relation = "unknown"
		case rel.TagName == cur:
			info.Relation = "current"
		case version.Compare(rel.TagName, cur) > 0:
			info.Relation = "newer"
		default:
			info.Relation = "older"
		}
		out = append(out, info)
	}
	return out, nil
}

func findAsset(rel *ghRelease, name string) *ghAsset {
	for i := range rel.Assets {
		if rel.Assets[i].Name == name {
			return &rel.Assets[i]
		}
	}
	return nil
}

// Check queries the latest release and reports whether an update is available.
func (m *Manager) Check(ctx context.Context) (*CheckResult, error) {
	rel, err := m.latestRelease(ctx)
	if err != nil {
		return nil, err
	}
	cur := version.Current()
	dev := version.IsDev()
	asset := findAsset(rel, assetName())

	res := &CheckResult{
		Current:     cur,
		Latest:      rel.TagName,
		Name:        rel.Name,
		Notes:       rel.Body,
		URL:         rel.HTMLURL,
		PublishedAt: rel.PublishedAt,
		AssetName:   assetName(),
		Dev:         dev,
	}
	if asset != nil {
		res.Downloadable = true
		res.AssetSize = asset.Size
	}
	// A dev build has no comparable version — surface the latest as installable
	// so operators can bootstrap onto a tagged build. Otherwise compare semver.
	if dev {
		res.UpdateAvailable = true
	} else {
		res.UpdateAvailable = version.Compare(rel.TagName, cur) > 0
	}
	return res, nil
}

// State returns a snapshot of the current updater progress.
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *Manager) setState(s Status, msg string, pct int, target string) {
	m.mu.Lock()
	m.state.Status = s
	m.state.Message = msg
	if pct >= 0 {
		m.state.Percent = pct
	}
	if target != "" {
		m.state.TargetVersion = target
	}
	m.mu.Unlock()
}

// Apply kicks off a self-update to the latest release in the background.
func (m *Manager) Apply(nowUnix int64) error { return m.ApplyVersion(nowUnix, "") }

// ApplyVersion installs a specific release tag, which may be OLDER than the
// running build — that is the point: it is how an operator gets off a release
// that turned out to be broken when the one-step rollback is not enough (the
// bad version has already been superseded, or the panel was reinstalled and no
// local backup exists).
//
// An empty tag means "latest", and keeps the original not-newer refusal. A
// pinned tag deliberately drops that check but replaces it with a stricter one:
// the release fetched must carry exactly the requested tag. The refusal existed
// because run() re-resolves "latest" and could therefore silently serve an
// older, known-vulnerable build; naming the version removes the "silently".
// Digest and signature verification are never skipped either way.
func (m *Manager) ApplyVersion(nowUnix int64, tag string) error {
	// Tag validation first: a malformed tag is a bad request whatever the
	// platform, and checking it here rather than behind the Linux gate keeps the
	// rejection identical (and testable) everywhere.
	tag = strings.TrimSpace(tag)
	if tag != "" && !validTag(tag) {
		return fmt.Errorf("版本号格式非法: %q", tag)
	}
	if runtime.GOOS != "linux" {
		return errors.New("自更新仅支持 Linux 部署；请在服务器上手动替换二进制")
	}
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return errors.New("已有更新任务在进行中")
	}
	m.running = true
	msg := "准备下载…"
	if tag != "" {
		msg = "准备下载 " + tag + "…"
	}
	m.state = State{Status: StatusDownloading, Message: msg, Percent: 0, StartedAt: nowUnix, TargetVersion: tag}
	m.mu.Unlock()

	go m.run(tag)
	return nil
}

func (m *Manager) fail(msg string) {
	m.setState(StatusFailed, msg, -1, "")
	m.mu.Lock()
	m.running = false
	m.mu.Unlock()
}

// run performs the full download → verify → swap → re-exec sequence. On success
// it never returns (the process image is replaced); any failure lands in
// StatusFailed with a message the UI shows.
// pinned is a release tag the admin explicitly chose, or "" for "latest".
func (m *Manager) run(pinned string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var rel *ghRelease
	var err error
	if pinned != "" {
		rel, err = m.releaseByTag(ctx, pinned)
	} else {
		rel, err = m.latestRelease(ctx)
	}
	if err != nil {
		m.fail("获取版本信息失败: " + err.Error())
		return
	}
	target := rel.TagName

	// Only the unpinned path refuses a non-newer version. run() re-fetches the
	// release rather than carrying the one Check() showed, so without this an
	// admin could be shown vX and served whatever /releases/latest returns at
	// apply time — including an older, known-vulnerable build, which installed
	// silently. A pinned tag is not silent, and releaseByTag has already bound
	// the answer to the exact version asked for.
	if pinned == "" && !isNewer(target, version.Current(), version.IsDev()) {
		m.fail(fmt.Sprintf("最新版本 %s 不比当前版本 %s 新，已取消更新", target, version.Current()))
		return
	}
	m.setState(StatusDownloading, "开始下载 "+target, 0, target)

	asset := findAsset(rel, assetName())
	if asset == nil {
		m.fail(fmt.Sprintf("该版本未提供适配当前架构的资产 (%s)", assetName()))
		return
	}
	wantDigest := parseSHA256(asset.Digest)
	if wantDigest == "" {
		m.fail("该版本资产缺少 sha256 摘要，出于安全考虑拒绝更新")
		return
	}
	// Fetch the detached signature before spending bandwidth on the binary.
	// Fail-closed: with a key compiled in, a release without a usable signature
	// is refused rather than silently falling back to the digest alone.
	var sig []byte
	if signingEnabled() {
		sigAsset := findAsset(rel, signatureAssetName(assetName()))
		if sigAsset == nil {
			m.fail(fmt.Sprintf("该版本缺少签名资产 (%s)，出于安全考虑拒绝更新", signatureAssetName(assetName())))
			return
		}
		if sig, err = m.fetchSignature(ctx, sigAsset.BrowserDownloadURL); err != nil {
			m.fail("下载签名失败: " + err.Error())
			return
		}
	}

	exePath, err := os.Executable()
	if err != nil {
		m.fail("无法定位当前程序路径: " + err.Error())
		return
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}

	// Download into a sibling temp file so the final rename is atomic (same fs).
	tmpPath := exePath + ".new"
	sum, err := m.download(ctx, asset.BrowserDownloadURL, tmpPath, asset.Size)
	if err != nil {
		_ = os.Remove(tmpPath)
		m.fail("下载失败: " + err.Error())
		return
	}

	m.setState(StatusVerifying, "校验完整性…", 100, target)
	if !strings.EqualFold(sum, wantDigest) {
		_ = os.Remove(tmpPath)
		m.fail("sha256 校验不匹配，已丢弃下载内容（可能被篡改或下载损坏）")
		return
	}
	// The digest proves the transfer was intact; only the signature proves the
	// project produced these bytes.
	if signingEnabled() {
		m.setState(StatusVerifying, "校验发布签名…", 100, target)
		body, rerr := os.ReadFile(tmpPath)
		if rerr != nil {
			_ = os.Remove(tmpPath)
			m.fail("读取下载内容失败: " + rerr.Error())
			return
		}
		if verr := verifySignature(body, sig); verr != nil {
			_ = os.Remove(tmpPath)
			m.fail("发布签名校验失败，已丢弃下载内容: " + verr.Error())
			return
		}
	}

	if err := m.installVerifiedBinary(exePath, tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		m.fail(err.Error())
		return
	}

	// One-click probe install copies whatever sits in QZ_PROBE_DIR. In-panel
	// self-update used to replace only the panel binary, so a later "一键升级"
	// still pushed the old agent (no -version flag). Refreshing here keeps the
	// hosted probes on the same release as the panel about to start.
	if note := refreshHostedProbes(ctx, m, rel, target); note != "" {
		m.setState(StatusInstalling, note, 100, target)
	}

	m.setState(StatusRestarting, "更新完成，正在重启服务…", 100, target)
	// Give the HTTP layer a beat to flush any in-flight status poll, then
	// re-exec. On success this call does not return.
	time.Sleep(600 * time.Millisecond)
	if err := restartSelf(exePath); err != nil {
		// The new binary is already installed but this process could not hand
		// over to it. Put the old one back: leaving the new binary on disk means
		// the next restart — whenever that happens — silently jumps versions to
		// something that has already failed to start once.
		msg := "重启失败: " + err.Error()
		if rerr := restoreBackup(exePath); rerr != nil {
			msg += "；回滚也失败了（" + rerr.Error() + "），请手动恢复"
		} else {
			msg += "；已回滚到更新前的版本，请手动重启服务"
		}
		m.fail(msg)
		return
	}
}

// copyFile is the fallback for backing up the running binary when a hard link
// isn't possible.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// fetchSignature retrieves a detached signature asset into memory.
func (m *Manager) fetchSignature(ctx context.Context, url string) ([]byte, error) {
	req, err := m.newRequest(ctx, url)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxSignatureBytes))
}

// download streams url into dst, returning the lowercase hex sha256 of the body
// and updating progress from the known content length.
func (m *Manager) download(ctx context.Context, url, dst string, size int64) (string, error) {
	return m.downloadFile(ctx, url, dst, size, true)
}

func (m *Manager) downloadFile(ctx context.Context, url, dst string, size int64, reportProgress bool) (string, error) {
	req, err := m.newRequest(ctx, url)
	if err != nil {
		return "", err
	}
	// Asset downloads are octet-streams, not the JSON API media type.
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > 0 {
		size = resp.ContentLength
	}

	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	writers := []io.Writer{f, h}
	if reportProgress {
		writers = append(writers, &progressWriter{m: m, total: size})
	}
	// Bounded: see maxAssetBytes. Read one byte past the cap so an oversized
	// body is reported as such instead of silently truncating into a digest
	// mismatch (a confusing "tampered" message for what is really a bad repo).
	n, err := io.Copy(io.MultiWriter(writers...), io.LimitReader(resp.Body, maxAssetBytes+1))
	if err != nil {
		return "", err
	}
	if n > maxAssetBytes {
		return "", fmt.Errorf("资产超过 %d MiB 上限，已中止下载", maxAssetBytes>>20)
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// progressWriter updates the manager's download percentage as bytes flow.
type progressWriter struct {
	m       *Manager
	total   int64
	written int64
	lastPct int
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n := len(b)
	p.written += int64(n)
	if p.total > 0 {
		pct := int(p.written * 100 / p.total)
		if pct > 100 {
			pct = 100
		}
		if pct != p.lastPct {
			p.lastPct = pct
			p.m.setState(StatusDownloading, "", pct, "")
		}
	}
	return n, nil
}

// parseSHA256 extracts the hex digest from a "sha256:<hex>" string (case- and
// prefix-tolerant), returning "" if it isn't a sha256 digest.
func parseSHA256(d string) string {
	d = strings.TrimSpace(d)
	if d == "" {
		return ""
	}
	if i := strings.IndexByte(d, ':'); i >= 0 {
		if !strings.EqualFold(d[:i], "sha256") {
			return ""
		}
		d = d[i+1:]
	}
	if len(d) != 64 {
		return ""
	}
	return strings.ToLower(d)
}

// probeDir is the directory this panel serves probe binaries from. Empty means
// the operator never configured hosting (QZ_PROBE_DIR unset and no default
// dist), so there is nothing for an in-panel update to refresh.
func probeDir() string {
	if dir := strings.TrimSpace(os.Getenv("QZ_PROBE_DIR")); dir != "" {
		return dir
	}
	return ""
}

// errProbeCurrent means the file on disk already matches the release digest,
// so there is nothing to download. Distinguishes "already good" from a failure
// so a boot-time sync stays quiet when install.sh (or a previous sync) did the
// work, without looking like a skipped asset.
var errProbeCurrent = errors.New("hosted probe already current")

// SyncHostedProbes aligns QZ_PROBE_DIR with the running panel's own release.
// In-panel self-update used to replace only the panel binary; the first boot
// of a version that knows how to refresh probes is therefore the moment the
// hosted agents actually catch up. Dev builds have no tag to fetch.
func (m *Manager) SyncHostedProbes(ctx context.Context) string {
	if m == nil || version.IsDev() {
		return ""
	}
	if probeDir() == "" {
		return ""
	}
	tag := strings.TrimSpace(version.Current())
	if !validTag(tag) {
		return ""
	}
	rel, err := m.releaseByTag(ctx, tag)
	if err != nil {
		return "同步托管探针失败: " + err.Error()
	}
	return refreshHostedProbes(ctx, m, rel, tag)
}

// refreshHostedProbes copies probe-linux-amd64/arm64 from the same release as
// the panel just installed. Failures are returned as a status note rather than
// aborting the update: the new panel is already on disk, and a missing probe
// asset must not undo that. Callers that need a hard failure use
// installHostedProbe directly in tests.
func refreshHostedProbes(ctx context.Context, m *Manager, rel *ghRelease, target string) string {
	if m == nil {
		return ""
	}
	m.probeMu.Lock()
	defer m.probeMu.Unlock()
	dir := probeDir()
	if dir == "" {
		return ""
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "面板已更新；探针目录不可用，一键安装仍会下发旧探针"
	}
	var ok, skipped []string
	for _, arch := range []string{"amd64", "arm64"} {
		name := probeAssetName(arch)
		asset := findAsset(rel, name)
		if asset == nil {
			skipped = append(skipped, name+" 缺失")
			continue
		}
		dst := filepath.Join(dir, name)
		if err := installHostedProbe(ctx, m, asset, dst); err != nil {
			if errors.Is(err, errProbeCurrent) {
				continue
			}
			skipped = append(skipped, name+"："+err.Error())
			continue
		}
		ok = append(ok, name)
	}
	if len(ok) == 0 && len(skipped) == 0 {
		return ""
	}
	msg := "面板 " + target + " 已安装"
	if len(ok) > 0 {
		msg += "；已刷新探针 " + strings.Join(ok, "、")
	}
	if len(skipped) > 0 {
		msg += "；未刷新 " + strings.Join(skipped, "、")
	}
	return msg
}

func installHostedProbe(ctx context.Context, m *Manager, asset *ghAsset, dst string) error {
	if asset == nil {
		return errors.New("缺少探针资产")
	}
	wantDigest := parseSHA256(asset.Digest)
	if wantDigest == "" {
		return errors.New("资产缺少 sha256 摘要")
	}
	if sum, _, err := fileSHA256(dst); err == nil && strings.EqualFold(sum, wantDigest) {
		return errProbeCurrent
	}
	tmp := dst + ".new"
	sum, err := m.downloadFile(ctx, asset.BrowserDownloadURL, tmp, asset.Size, false)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if !strings.EqualFold(sum, wantDigest) {
		_ = os.Remove(tmp)
		return errors.New("sha256 校验不匹配")
	}
	// Probe assets are not signed in the release workflow (only the panel
	// binary is). Digest check is the same bar install.sh already uses.
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
