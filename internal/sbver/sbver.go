// Package sbver interprets the output of `sing-box version` and judges whether
// a node's binary is fit for what the panel needs from it.
//
// The panel already ran this command on every node — to decide whether the
// v2ray_api plugin is present — and threw the version away. An operator who
// installed sing-box with the one-line script had no way to see, from the
// panel, which version a node ended up with, whether it still satisfies the
// config the panel generates, or whether the node was quietly left behind.
package sbver

import (
	"strconv"
	"strings"
)

// MinSupported is the oldest sing-box the generated config is valid for.
//
// Not a guess: the config uses the typed DNS server form and the route rule
// actions that arrived in 1.12, and `sing-box check` on an older binary fails —
// which on a node means the panel silently stops being able to deploy anything
// to it, because an invalid config is never swapped in.
const MinSupported = "1.12.0"

// VisionFramingFixVersion is the reviewed build with sing-vmess commit
// 9b95ab8c9478. Stock 1.14.2 still embeds the affected dependency. Version
// ordering cannot establish this capability, including for future releases.
const VisionFramingFixVersion = "1.14.2+qz-vmess.9b95ab8c9478"

// TransportReadBufferFixVersion additionally contains the reviewed upstream
// transport buffering fix 07512b10. Older Vision-only builds do not prove it.
const TransportReadBufferFixVersion = "1.14.2+qz-vmess.9b95ab8c9478-transport.07512b10"

// TrojanHandshakeFixVersion is the current release core. It additionally
// carries the qing-zhou Trojan handshake maintenance patch (issue #87, patch
// sha256 b85be9da…; not an upstream fix). Vision-only and transport-only markers
// do not prove it, and neither does any higher or suffixed version.
const TrojanHandshakeFixVersion = "1.14.2+qz-vmess.9b95ab8c9478-transport.07512b10-trojan.b85be9da"

func HasVisionFramingFix(version string) bool {
	return version == VisionFramingFixVersion || version == TransportReadBufferFixVersion || version == TrojanHandshakeFixVersion
}

func HasTransportReadBufferFix(version string) bool {
	return version == TransportReadBufferFixVersion || version == TrojanHandshakeFixVersion
}

func HasTrojanHandshakeFix(version string) bool {
	return version == TrojanHandshakeFixVersion
}

// Info is what the panel knows about one node's sing-box.
type Info struct {
	// Version is the bare version, e.g. "1.13.18". Empty when the output could
	// not be parsed — treated as unknown, never as "old".
	Version string `json:"version"`
	// HasV2RayAPI reports the with_v2ray_api build tag. Without it the node
	// carries traffic but reports none, so quotas silently never apply.
	HasV2RayAPI bool `json:"has_v2ray_api"`
	// HasVisionFramingFix is true only for the explicitly reviewed core marker.
	HasVisionFramingFix bool `json:"has_vision_framing_fix"`
	// WS/HTTPUpgrade capability is independent from the older Vision-only fix.
	HasTransportReadBufferFix bool `json:"has_transport_read_buffer_fix"`
	// Trojan fragmented-handshake capability (#87) is independent of both.
	HasTrojanHandshakeFix bool `json:"has_trojan_handshake_fix"`
	// Raw is the first line of output, kept so an unparseable answer can still
	// be shown to a human instead of an empty box.
	Raw string `json:"raw"`
}

// Parse reads `sing-box version` output. It is deliberately tolerant: the
// command prints a version line, a blank line, then Environment/Tags/Revision,
// and the shape has changed across releases.
func Parse(out string) Info {
	info := Info{HasV2RayAPI: strings.Contains(out, "with_v2ray_api")}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if info.Raw == "" {
			info.Raw = line
		}
		// "sing-box version 1.13.18"
		if v, ok := versionFrom(line); ok && info.Version == "" {
			info.Version = v
		}
	}
	info.HasVisionFramingFix = HasVisionFramingFix(info.Version)
	info.HasTransportReadBufferFix = HasTransportReadBufferFix(info.Version)
	info.HasTrojanHandshakeFix = HasTrojanHandshakeFix(info.Version)
	return info
}

// versionFrom extracts the version token from a line like
// "sing-box version 1.13.18" — the token after the word "version".
func versionFrom(line string) (string, bool) {
	fields := strings.Fields(line)
	for i, f := range fields {
		if strings.EqualFold(f, "version") && i+1 < len(fields) {
			v := strings.TrimPrefix(strings.TrimPrefix(fields[i+1], "v"), "V")
			if isVersionish(v) {
				return v, true
			}
		}
	}
	return "", false
}

// isVersionish accepts a leading numeric segment, so "1.13.18" and
// "1.14.0-beta.12" both qualify while "unknown" does not.
func isVersionish(v string) bool {
	if v == "" || v[0] < '0' || v[0] > '9' {
		return false
	}
	return true
}

// TooOld reports whether the version is below MinSupported. An unparseable or
// empty version returns false: "we could not tell" must not be rendered as a
// problem the operator has to act on.
func (i Info) TooOld() bool {
	if i.Version == "" {
		return false
	}
	return Compare(i.Version, MinSupported) < 0
}

// Compare orders two sing-box versions, ignoring a leading "v" and any
// pre-release suffix, so 1.14.0-beta.12 compares equal to 1.14.0. Missing
// segments count as zero, so "1.13" == "1.13.0".
func Compare(a, b string) int {
	pa, pb := segments(a), segments(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func segments(v string) []int {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil
	}
	out := make([]int, 0, 3)
	for _, p := range strings.Split(v, ".") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			n = 0
		}
		out = append(out, n)
	}
	return out
}
