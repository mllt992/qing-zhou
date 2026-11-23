package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"qingzhou/internal/singbox"
	"qingzhou/internal/store"
)

const echKeyTokenPrefix = "qzech1:"

type certTlsECHReq struct {
	Enabled    bool
	KeyToken   string
	PublicName string
	MaxVersion string
}

func (a *API) echTokenAEAD() (cipher.AEAD, error) {
	sum := sha256.Sum256(append([]byte("qz-ech-key-token-v1:"), a.secret...))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealECHKeyToken wraps an ECH KEYS PEM block in an opaque AES-256-GCM token so
// the browser can carry a freshly generated keypair back to Save without ever
// seeing the plaintext private key.
func (a *API) sealECHKeyToken(keyPEM string) (string, error) {
	aead, err := a.echTokenAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(keyPEM), []byte(echKeyTokenPrefix))
	return echKeyTokenPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (a *API) unsealECHKeyToken(token string) (string, error) {
	if !strings.HasPrefix(token, echKeyTokenPrefix) {
		return "", errors.New("无效的 ECH 密钥令牌，请重新生成")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, echKeyTokenPrefix))
	if err != nil {
		return "", errors.New("无效的 ECH 密钥令牌，请重新生成")
	}
	aead, err := a.echTokenAEAD()
	if err != nil {
		return "", err
	}
	ns := aead.NonceSize()
	if len(raw) <= ns {
		return "", errors.New("无效的 ECH 密钥令牌，请重新生成")
	}
	plain, err := aead.Open(nil, raw[:ns], raw[ns:], []byte(echKeyTokenPrefix))
	if err != nil {
		return "", errors.New("ECH 密钥令牌校验失败，请重新生成")
	}
	return string(plain), nil
}

// POST /api/admin/sb/tls/ech-keygen — generate an ECH keypair for the given
// public_name (cleartext outer SNI). Returns ONLY the public ECHConfigList and
// an opaque server-sealed key_token; the private key never leaves the backend.
func (a *API) handleAdminGenerateECHKeypair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PublicName string `json:"public_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	pubName, err := singbox.ValidateECHPublicName(req.PublicName)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	configPEM, keyPEM, err := singbox.GenerateECHKeypair(pubName)
	if err != nil {
		fail(w, http.StatusInternalServerError, "生成 ECH 密钥失败")
		return
	}
	keyToken, err := a.sealECHKeyToken(keyPEM)
	if err != nil {
		fail(w, http.StatusInternalServerError, "封装 ECH 密钥失败")
		return
	}
	ok(w, J{
		"public_name":   pubName,
		"config_pem":    configPEM,
		"config_base64": singbox.ECHConfigBase64(configPEM),
		"key_token":     keyToken,
	})
}

// applyCertTlsECH resolves ECH server/client blocks for handleAdminSaveCertTls.
// Private keys stay server-side: supplied either via an opaque key_token, kept
// from the existing DB profile on edit, or generated on demand from public_name.
func (a *API) applyCertTlsECH(existingID int64, req certTlsECHReq, server, client map[string]interface{}) error {
	if !req.Enabled {
		return nil
	}
	if tlsVersion(req.MaxVersion) == "1.2" {
		return errors.New("启用 ECH 需要 TLS 1.3，最高 TLS 版本不能设为 1.2")
	}
	var keyPEM string
	switch {
	case strings.TrimSpace(req.KeyToken) != "":
		var err error
		keyPEM, err = a.unsealECHKeyToken(strings.TrimSpace(req.KeyToken))
		if err != nil {
			return err
		}
	case existingID != 0:
		if cur, _ := a.st.GetSbTls(existingID); cur != nil && !cur.DecryptFailed {
			var cs map[string]interface{}
			if json.Unmarshal([]byte(cur.ServerJSON), &cs) == nil {
				if ech, ok := cs["ech"].(map[string]interface{}); ok {
					keyPEM = singbox.JoinPEMLines(ech["key"])
				}
			}
		}
		if keyPEM == "" && strings.TrimSpace(req.PublicName) != "" {
			_, generatedKey, err := singbox.GenerateECHKeypair(req.PublicName)
			if err != nil {
				return err
			}
			keyPEM = generatedKey
		}
	case strings.TrimSpace(req.PublicName) != "":
		_, generatedKey, err := singbox.GenerateECHKeypair(req.PublicName)
		if err != nil {
			return err
		}
		keyPEM = generatedKey
	}
	if keyPEM == "" {
		return errors.New("启用 ECH 需先生成 ECH 密钥（填写公开名称）")
	}
	configPEM, _, err := singbox.ECHConfigFromKeyPEM(keyPEM)
	if err != nil {
		return err
	}
	server["ech"] = map[string]interface{}{
		"enabled": true,
		"key":     singbox.PEMLines(keyPEM),
	}
	client["ech"] = map[string]interface{}{
		"enabled": true,
		"config":  singbox.PEMLines(configPEM),
	}
	return nil
}

// sbTlsAPIView renders a store.SbTls record for HTTP responses with server-side
// ECH private keys stripped so `BEGIN ECH KEYS` never appears in API output.
func sbTlsAPIView(t *store.SbTls) map[string]interface{} {
	if t == nil {
		return nil
	}
	serverJSON := t.ServerJSON
	echEnabled := false
	echHasKey := false
	echPublicName := ""
	echConfig := ""

	var sj map[string]interface{}
	if t.ServerJSON != "" && json.Unmarshal([]byte(t.ServerJSON), &sj) == nil {
		if ech, ok := sj["ech"].(map[string]interface{}); ok {
			echEnabled, _ = ech["enabled"].(bool)
			keyPEM := singbox.JoinPEMLines(ech["key"])
			if keyPEM != "" {
				echHasKey = true
				if cfgPEM, pub, err := singbox.ECHConfigFromKeyPEM(keyPEM); err == nil {
					echConfig = singbox.ECHConfigBase64(cfgPEM)
					echPublicName = pub
				}
			}
			// Never leak ECH private keys into API responses.
			delete(ech, "key")
			if redacted, err := json.Marshal(sj); err == nil {
				serverJSON = string(redacted)
			}
		}
	}
	var cj map[string]interface{}
	if t.ClientJSON != "" && json.Unmarshal([]byte(t.ClientJSON), &cj) == nil {
		if ech, ok := cj["ech"].(map[string]interface{}); ok {
			if b64 := singbox.ECHConfigBase64(ech["config"]); b64 != "" {
				echConfig = b64
				if pub := singbox.ECHPublicNameFromConfig(b64); pub != "" {
					echPublicName = pub
				}
			}
		}
	}

	return map[string]interface{}{
		"id":              t.ID,
		"server_id":       t.ServerID,
		"name":            t.Name,
		"mode":            t.Mode,
		"server_json":     serverJSON,
		"client_json":     t.ClientJSON,
		"cert_id":         t.CertID,
		"sort_order":      t.SortOrder,
		"created_at":      t.CreatedAt,
		"updated_at":      t.UpdatedAt,
		"ech_enabled":     echEnabled,
		"ech_has_key":     echHasKey,
		"ech_public_name": echPublicName,
		"ech_config":      echConfig,
	}
}

// preserveStoredECHKey keeps an existing profile's ECH private key when raw
// SaveSbTls receives an updated ServerJSON whose ech.key was redacted on GET.
func (a *API) preserveStoredECHKey(t *store.SbTls) {
	if t == nil || t.ID == 0 || t.ServerJSON == "" {
		return
	}
	var incoming map[string]interface{}
	if json.Unmarshal([]byte(t.ServerJSON), &incoming) != nil {
		return
	}
	ech, ok := incoming["ech"].(map[string]interface{})
	if !ok {
		return
	}
	if enabled, _ := ech["enabled"].(bool); !enabled {
		return
	}
	if singbox.JoinPEMLines(ech["key"]) != "" {
		return
	}
	cur, _ := a.st.GetSbTls(t.ID)
	if cur == nil || cur.DecryptFailed {
		return
	}
	var stored map[string]interface{}
	if json.Unmarshal([]byte(cur.ServerJSON), &stored) != nil {
		return
	}
	if storedECH, ok := stored["ech"].(map[string]interface{}); ok {
		if keyVal, exists := storedECH["key"]; exists {
			ech["key"] = keyVal
			if raw, err := json.Marshal(incoming); err == nil {
				t.ServerJSON = string(raw)
			}
		}
	}
}

// redactPreviewECHKeys replaces any inbound tls.ech.key in GET /api/admin/sb/preview
// so ECH private keys never appear in HTTP responses while keeping the preview
// structure readable.
func redactPreviewECHKeys(cfg []byte) []byte {
	var doc map[string]interface{}
	if err := json.Unmarshal(cfg, &doc); err != nil {
		return cfg
	}
	inbounds, ok := doc["inbounds"].([]interface{})
	if !ok {
		return cfg
	}
	changed := false
	for _, item := range inbounds {
		ib, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		tlsMap, ok := ib["tls"].(map[string]interface{})
		if !ok {
			continue
		}
		echMap, ok := tlsMap["ech"].(map[string]interface{})
		if !ok {
			continue
		}
		if _, hasKey := echMap["key"]; hasKey {
			echMap["key"] = []string{"[REDACTED]"}
			changed = true
		}
	}
	if !changed {
		return cfg
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return cfg
	}
	return out
}
