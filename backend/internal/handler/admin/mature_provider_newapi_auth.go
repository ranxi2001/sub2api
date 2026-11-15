// Adapted from mizaawa/zayuapi v0.2.219, a71aa9615ccb55a19973908c9f261b5ba25a48c6.
// LGPL v3; upstream authors retain their copyrights. See mature_provider_NOTICE.md.
package admin

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net/http"
	"strings"
	"unicode"
)

// New API dashboard sign-in. Releases since July 2026 authenticate dashboard
// requests with a Bearer credential alone: a scoped personal access token
// (nap_…) or the short-lived access token that /api/user/login returns.
// Earlier releases accept a session cookie or a legacy system access token
// and also require the account ID in the New-Api-User header.

// newAPIPersonalTokenPrefix marks a scoped personal access token.
const newAPIPersonalTokenPrefix = "nap_"

func newAPIPersonalToken(token string) bool {
	return strings.HasPrefix(strings.TrimSpace(token), newAPIPersonalTokenPrefix)
}

// newAPISignIn chooses how r authenticates every later dashboard request:
// the balance probe and the group, key list, key reveal and creation calls.
func newAPISignIn(ctx context.Context, r *providerRemote, record providerRecord, secret string) error {
	r.token, r.tokenType, r.userID = "", "", ""
	if record.AuthMode != "password" {
		token, err := decodeProviderToken(secret)
		if err != nil || token.AccessToken == "" {
			return errProviderStoredLogin
		}
		r.token, r.tokenType = token.AccessToken, token.TokenType
		if !newAPIPersonalToken(token.AccessToken) {
			// Older releases match a legacy token against this account ID.
			r.userID = record.UserID
		}
		return nil
	}
	body, err := newAPILoginBody(ctx, r, record.Account, secret)
	if err != nil {
		return err
	}
	login, err := r.request(ctx, http.MethodPost, "/api/user/login", body)
	if err != nil {
		return err
	}
	if newAPIVerificationRequired(login) {
		return errProviderTwoFactor
	}
	if access, _ := login["access_token"].(string); strings.TrimSpace(access) != "" {
		// Current releases return a short-lived Bearer token. The refresh cookie
		// set beside it serves only the browser's own refresh flow.
		r.token = strings.TrimSpace(access)
		if tokenType, _ := login["token_type"].(string); validProviderTokenType(tokenType) {
			r.tokenType = tokenType
		}
		return nil
	}
	// Older releases keep the session cookie in the client's jar and expect the
	// signed-in account's ID on every dashboard request.
	r.userID = providerText(login["id"])
	return nil
}

// newAPIVerificationRequired reports a password login that stopped at a
// second factor: require_verification today, require_2fa in older releases.
func newAPIVerificationRequired(login map[string]any) bool {
	for _, field := range []string{"require_verification", "require_2fa"} {
		if required, _ := login[field].(bool); required {
			return true
		}
	}
	return false
}

// newAPILoginBody builds the password login. A site that turns on login
// password encryption publishes an RSA key and refuses a plain password;
// releases without the key endpoint, or with encryption off, take it plain.
func newAPILoginBody(ctx context.Context, r *providerRemote, account, password string) (map[string]string, error) {
	plain := map[string]string{"username": account, "password": password}
	key, err := r.request(ctx, http.MethodGet, "/api/user/login/encryption-key", nil)
	if err != nil {
		return plain, nil
	}
	if enabled, _ := key["enabled"].(bool); !enabled {
		return plain, nil
	}
	keyID, _ := key["kid"].(string)
	publicKey, _ := key["public_key"].(string)
	ciphertext, err := encryptNewAPIPassword(password, publicKey, keyID)
	if err != nil {
		return nil, errProviderResponse
	}
	return map[string]string{"username": account, "password_encrypted": ciphertext, "encryption_key_id": keyID}, nil
}

// encryptNewAPIPassword mirrors the New API web console: RSA-OAEP with
// SHA-256 when the password fits the key, otherwise the "v2" envelope that
// wraps a fresh AES-256-GCM key with RSA-OAEP.
func encryptNewAPIPassword(password, publicKeyPEM, keyID string) (string, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil || keyID == "" {
		return "", errors.New("invalid password encryption key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", err
	}
	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return "", errors.New("password encryption key is not RSA")
	}
	if len(password) <= publicKey.Size()-2*sha256.Size-2 {
		ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, []byte(password), nil)
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(ciphertext), nil
	}
	secret := make([]byte, 32)
	nonce := make([]byte, 12)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	wrapped, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, secret, []byte("password-v2"))
	if err != nil {
		return "", err
	}
	aesBlock, err := aes.NewCipher(secret)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(aesBlock)
	if err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(password), []byte("password-v2:"+keyID))
	encode := base64.StdEncoding.EncodeToString
	return strings.Join([]string{"v2", encode(wrapped), encode(nonce), encode(sealed)}, "."), nil
}

// newAPIAccessError reads the stable code that a current New API release puts
// on a refused dashboard credential. Other responses yield nil.
func newAPIAccessError(envelope map[string]any) error {
	code, _ := envelope["code"].(string)
	switch code {
	case "ACCESS_TOKEN_SCOPE_DENIED":
		params, _ := envelope["message_params"].(map[string]any)
		scope, _ := params["scope"].(string)
		if !validProviderScope(scope) {
			scope = ""
		}
		return &providerScopeError{scope: scope}
	case "ACCESS_TOKEN_EXPIRED":
		return errProviderTokenExpired
	case "ACCESS_TOKEN_LEGACY_RETIRED":
		return errProviderTokenRetired
	}
	return nil
}

// Only known permission labels may enter a public error. Arbitrary upstream
// strings, even identifier-shaped ones, can contain a reflected credential.
func validProviderScope(scope string) bool {
	switch scope {
	case "profile:read", "profile:write", "api_key:read", "api_key:write", "api_key:create", "api_key:update", "api_key:delete", "api_key:reveal", "usage:read", "group:read", "groups:read", "log:read", "model:read":
		return true
	default:
		return false
	}
}

// newAPIAccountID returns the account ID in a /api/user/self profile, or ""
// when the site reports none.
func newAPIAccountID(profile map[string]any) string {
	id := providerText(profile["id"])
	if id == "" || id == "0" || len(id) > 128 || strings.IndexFunc(id, unicode.IsControl) >= 0 {
		return ""
	}
	return id
}
