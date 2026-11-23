package singbox

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/cryptobyte"
)

const (
	echExtensionVersion    = 0xfe0d
	echKEMX25519HKDFSHA256 = 0x0020
	echKDFHKDFSHA256       = 0x0001
	echAEADAES128GCM       = 0x0001
	echAEADAES256GCM       = 0x0002
	echAEADChaCha20Poly    = 0x0003
)

// ValidateECHPublicName checks that publicName is a safe, non-empty ASCII
// hostname suitable for the cleartext outer SNI of an ECHConfig.
func ValidateECHPublicName(publicName string) (string, error) {
	name := strings.TrimSpace(publicName)
	if name == "" {
		return "", errors.New("ECH 公开名称（外层 SNI）不能为空")
	}
	if len(name) > 253 {
		return "", errors.New("ECH 公开名称过长")
	}
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") {
		return "", fmt.Errorf("ECH 公开名称格式无效: %q", name)
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		ok := (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
			(b >= '0' && b <= '9') || b == '-' || b == '.'
		if !ok {
			return "", fmt.Errorf("ECH 公开名称含非法字符: %q", name)
		}
	}
	return name, nil
}

// GenerateECHKeypair generates an X25519 ECH keypair and ECHConfigList encoded
// in the exact PEM formats used by `sing-box generate ech-keypair <public_name>`
// (PEM blocks "ECH CONFIGS" and "ECH KEYS").
func GenerateECHKeypair(publicName string) (configPEM, keyPEM string, err error) {
	name, err := ValidateECHPublicName(publicName)
	if err != nil {
		return "", "", err
	}
	echKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	echConfig, err := marshalECHConfig(0, echKey.PublicKey().Bytes(), name, 0)
	if err != nil {
		return "", "", err
	}
	var configBuilder cryptobyte.Builder
	configBuilder.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
		b.AddBytes(echConfig)
	})
	configBytes, err := configBuilder.Bytes()
	if err != nil {
		return "", "", err
	}
	var keyBuilder cryptobyte.Builder
	keyBuilder.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
		b.AddBytes(echKey.Bytes())
	})
	keyBuilder.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
		b.AddBytes(echConfig)
	})
	keyBytes, err := keyBuilder.Bytes()
	if err != nil {
		return "", "", err
	}
	configPEM = string(pem.EncodeToMemory(&pem.Block{Type: "ECH CONFIGS", Bytes: configBytes}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "ECH KEYS", Bytes: keyBytes}))
	return configPEM, keyPEM, nil
}

func marshalECHConfig(id uint8, pubKey []byte, publicName string, maxNameLen uint8) ([]byte, error) {
	var builder cryptobyte.Builder
	builder.AddUint16(echExtensionVersion)
	builder.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
		b.AddUint8(id)
		b.AddUint16(echKEMX25519HKDFSHA256)
		b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
			b.AddBytes(pubKey)
		})
		b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
			for _, aeadID := range []uint16{echAEADAES128GCM, echAEADAES256GCM, echAEADChaCha20Poly} {
				b.AddUint16(echKDFHKDFSHA256)
				b.AddUint16(aeadID)
			}
		})
		b.AddUint8(maxNameLen)
		b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) {
			b.AddBytes([]byte(publicName))
		})
		b.AddUint16(0) // extensions
	})
	return builder.Bytes()
}

// ParseECHKeysPEM parses a sing-box "ECH KEYS" PEM block into Go stdlib
// EncryptedClientHelloKey entries.
func ParseECHKeysPEM(keyPEM string) ([]tls.EncryptedClientHelloKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(keyPEM)))
	if block == nil || block.Type != "ECH KEYS" {
		return nil, errors.New("无效的 ECH 私钥 PEM")
	}
	s := cryptobyte.String(block.Bytes)
	var keys []tls.EncryptedClientHelloKey
	for !s.Empty() {
		var k tls.EncryptedClientHelloKey
		if !s.ReadUint16LengthPrefixed((*cryptobyte.String)(&k.PrivateKey)) {
			return nil, errors.New("ECH 私钥解析失败")
		}
		if !s.ReadUint16LengthPrefixed((*cryptobyte.String)(&k.Config)) {
			return nil, errors.New("ECH 配置解析失败")
		}
		if len(k.PrivateKey) == 0 || len(k.Config) == 0 {
			return nil, errors.New("ECH 密钥或配置为空")
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, errors.New("ECH 密钥为空")
	}
	return keys, nil
}

// ECHConfigFromKeyPEM extracts the matching ECHConfigList PEM and outer
// public_name embedded inside an "ECH KEYS" PEM block.
func ECHConfigFromKeyPEM(keyPEM string) (configPEM, publicName string, err error) {
	keys, err := ParseECHKeysPEM(keyPEM)
	if err != nil {
		return "", "", err
	}
	var configBuilder cryptobyte.Builder
	configBuilder.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
		for _, k := range keys {
			b.AddBytes(k.Config)
		}
	})
	configBytes, err := configBuilder.Bytes()
	if err != nil {
		return "", "", err
	}
	configPEM = string(pem.EncodeToMemory(&pem.Block{Type: "ECH CONFIGS", Bytes: configBytes}))
	publicName = parseECHConfigPublicName(keys[0].Config)
	return configPEM, publicName, nil
}

// PEMLines splits a PEM string into trimmed non-empty lines, matching sing-box's
// Listable[string] representation for tls.ech.key and tls.ech.config.
func PEMLines(pemStr string) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(pemStr), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// JoinPEMLines joins a sing-box string-or-string-slice PEM field into one PEM string.
func JoinPEMLines(v interface{}) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case []string:
		return strings.TrimSpace(strings.Join(x, "\n"))
	case []interface{}:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return ""
	}
}

// ECHConfigBase64 extracts and validates the bare base64 ECHConfigList from a
// PEM string, a slice of PEM lines (sing-box client JSON), or a bare base64 string.
// Returns "" if empty or malformed.
func ECHConfigBase64(config interface{}) string {
	raw := JoinPEMLines(config)
	if raw == "" {
		return ""
	}
	var b strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "-----") {
			continue
		}
		b.WriteString(s)
	}
	b64 := strings.ReplaceAll(b.String(), " ", "+")
	if !isValidECHConfigListBase64(b64) {
		return ""
	}
	return b64
}

// ECHConfigPEMLines wraps a validated bare base64 ECHConfigList into PEM lines
// for sing-box outbound tls.ech.config.
func ECHConfigPEMLines(base64Config string) []string {
	cfg := ECHConfigBase64(base64Config)
	if cfg == "" {
		return nil
	}
	return []string{"-----BEGIN ECH CONFIGS-----", cfg, "-----END ECH CONFIGS-----"}
}

// ECHPublicNameFromConfig extracts the outer public_name from an ECHConfigList
// (PEM string, line slice, or bare base64). Returns "" when unparseable.
func ECHPublicNameFromConfig(config interface{}) string {
	b64 := ECHConfigBase64(config)
	if b64 == "" {
		return ""
	}
	list, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return ""
	}
	s := cryptobyte.String(list)
	var body cryptobyte.String
	if !s.ReadUint16LengthPrefixed(&body) || !s.Empty() {
		return ""
	}
	return parseECHConfigPublicName(body)
}

// ParseECHLinkParam decodes a share link's `ech=` value following S-UI #1339:
//   - a bare base64 ECHConfigList -> (configBase64, "")
//   - Xray's "domain+DNS" form    -> ("", queryServerName)
//   - anything else               -> ("", "")
func ParseECHLinkParam(value string) (configBase64, queryServerName string) {
	// A raw '+' in a URL query string decodes to a space.
	v := strings.ReplaceAll(strings.TrimSpace(value), " ", "+")
	if v == "" {
		return "", ""
	}
	if isValidECHConfigListBase64(v) {
		return v, ""
	}
	if name, _, found := strings.Cut(v, "+"); found {
		name = strings.TrimSpace(name)
		if strings.Contains(name, ".") {
			if valid, err := ValidateECHPublicName(name); err == nil {
				return "", valid
			}
		}
	}
	return "", ""
}

func isValidECHConfigListBase64(b64 string) bool {
	if b64 == "" {
		return false
	}
	list, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(list) <= 2 {
		return false
	}
	return int(list[0])<<8|int(list[1]) == len(list)-2
}

func parseECHConfigPublicName(echConfig []byte) string {
	s := cryptobyte.String(echConfig)
	var version uint16
	var contents cryptobyte.String
	if !s.ReadUint16(&version) || version != echExtensionVersion || !s.ReadUint16LengthPrefixed(&contents) {
		return ""
	}
	var configID uint8
	var kemID uint16
	var pubKey, cipherSuites, pubName cryptobyte.String
	var maxNameLen uint8
	if !contents.ReadUint8(&configID) ||
		!contents.ReadUint16(&kemID) ||
		!contents.ReadUint16LengthPrefixed(&pubKey) ||
		!contents.ReadUint16LengthPrefixed(&cipherSuites) ||
		!contents.ReadUint8(&maxNameLen) ||
		!contents.ReadUint8LengthPrefixed(&pubName) {
		return ""
	}
	return string(pubName)
}
