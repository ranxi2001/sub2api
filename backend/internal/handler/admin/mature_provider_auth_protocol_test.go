// Adapted from mizaawa/zayuapi a71aa9615ccb55a19973908c9f261b5ba25a48c6 (LGPL v3).
// See mature_provider_NOTICE.md and docs/mature-provider-provenance.json.
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
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testNewAPIPersonalToken = "nap_EXAMPLE_PERSONAL_ACCESS_TOKEN"

func writeProviderJSON(w http.ResponseWriter, status int, raw string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(raw))
}
func newAPITokenSecret(t *testing.T, token string) string {
	t.Helper()
	secret, err := encodeProviderToken(token, "", "Bearer")
	require.NoError(t, err)
	return secret
}

func TestMatureProtocolNewAPIPersonalAccessTokenProbeSendsOnlyBearer(t *testing.T) {
	var paths []string
	var mu sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		assert.Empty(t, r.Header.Values("New-Api-User"), r.URL.Path)
		switch r.URL.Path {
		case "/api/user/self":
			assert.Equal(t, "Bearer "+testNewAPIPersonalToken, r.Header.Get("Authorization"))
			writeProviderJSON(w, http.StatusOK, `{"success":true,"message":"","data":{"id":42,"username":"ops","quota":1000000,"used_quota":250000,"group":"default"}}`)
		case "/api/status":
			assert.Empty(t, r.Header.Get("Authorization"), "the public status endpoint never receives the token")
			writeProviderJSON(w, http.StatusOK, `{"success":true,"message":"","data":{"quota_per_unit":500000}}`)
		case "/api/log/self/stat":
			assert.Equal(t, "Bearer "+testNewAPIPersonalToken, r.Header.Get("Authorization"))
			assert.Equal(t, "2", r.URL.Query().Get("type"))
			assert.NotEmpty(t, r.URL.Query().Get("start_timestamp"))
			assert.NotEmpty(t, r.URL.Query().Get("end_timestamp"))
			writeProviderJSON(w, http.StatusOK, `{"success":true,"message":"","data":{"quota":100000,"rpm":0,"tpm":0}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	probe, err := probeProviderAccount(context.Background(), providerRecord{SiteType: "newapi", AuthMode: "token", BaseURL: server.URL},
		newAPITokenSecret(t, testNewAPIPersonalToken), server.Client())
	require.NoError(t, err)
	require.Equal(t, 2.0, *probe.metrics.Balance)
	require.Equal(t, 0.5, *probe.metrics.TotalSpend)
	require.Equal(t, 0.2, *probe.metrics.TodaySpend)
	require.Equal(t, "42", probe.userID)
	require.Equal(t, []string{"/api/user/self", "/api/status", "/api/log/self/stat"}, paths)
}

func TestMatureProtocolNewAPIPasswordLoginStopsAtVerification(t *testing.T) {
	for _, data := range []string{
		`{"require_verification":true,"flow_token":"flow","expires_at":1893456000,"methods":[{"method":"totp","available":true}]}`,
		`{"require_2fa":true}`,
	} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/user/login" {
				writeProviderJSON(w, http.StatusOK, `{"success":true,"message":"","data":`+data+`}`)
				return
			}
			http.NotFound(w, r)
		}))
		_, err := probeProviderAccount(context.Background(), providerRecord{SiteType: "newapi", AuthMode: "password", BaseURL: server.URL, Account: "ops"}, "dashboard-password", server.Client())
		server.Close()
		require.ErrorIs(t, err, errProviderTwoFactor, data)

	}
}

// decryptNewAPIPassword mirrors New API's common.DecryptPassword.
func decryptNewAPIPassword(key *rsa.PrivateKey, keyID, payload string) (string, error) {
	if !strings.HasPrefix(payload, "v2.") {
		ciphertext, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return "", err
		}
		plain, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, key, ciphertext, nil)
		return string(plain), err
	}
	parts := strings.Split(payload, ".")
	if len(parts) != 4 {
		return "", errors.New("malformed v2 payload")
	}
	decoded := make([][]byte, 3)
	for i, part := range parts[1:] {
		value, err := base64.StdEncoding.Strict().DecodeString(part)
		if err != nil {
			return "", err
		}
		decoded[i] = value
	}
	secret, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, key, decoded[0], []byte("password-v2"))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(secret)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, decoded[1], decoded[2], []byte("password-v2:"+keyID))
	return string(plain), err
}

func TestMatureProtocolNewAPIPasswordLoginEncryptsWhenTheSiteRequiresIt(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	publicKey, err := json.Marshal(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
	require.NoError(t, err)
	const keyID = "0123456789abcdef0123456789abcdef"

	for _, password := range []string{"dashboard-password", strings.Repeat("长密码", 40)} {
		var received sync.Map
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/user/login/encryption-key":
				writeProviderJSON(w, http.StatusOK, `{"success":true,"message":"","data":{"enabled":true,"kid":"`+keyID+`","public_key":`+string(publicKey)+`}}`)
			case "/api/user/login":
				var body map[string]string
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				_, plain := body["password"]
				assert.False(t, plain, "an encrypted login must not also send the plain password")
				assert.Equal(t, keyID, body["encryption_key_id"])
				decrypted, err := decryptNewAPIPassword(key, body["encryption_key_id"], body["password_encrypted"])
				assert.NoError(t, err)
				received.Store("password", decrypted)
				received.Store("v2", strings.HasPrefix(body["password_encrypted"], "v2."))
				writeProviderJSON(w, http.StatusOK, `{"success":true,"message":"","data":{"access_token":"short-lived-access","token_type":"Bearer","user":{"id":5}}}`)
			case "/api/user/self":
				writeProviderJSON(w, http.StatusOK, `{"success":true,"message":"","data":{"id":5,"quota":500000}}`)
			case "/api/status":
				writeProviderJSON(w, http.StatusOK, `{"success":true,"message":"","data":{"quota_per_unit":500000}}`)
			default:
				http.NotFound(w, r)
			}
		}))
		probe, err := probeProviderAccount(context.Background(), providerRecord{SiteType: "newapi", AuthMode: "password", BaseURL: server.URL, Account: "ops"}, password, server.Client())
		server.Close()
		require.NoError(t, err)
		require.Equal(t, 1.0, *probe.metrics.Balance)
		decrypted, _ := received.Load("password")
		require.Equal(t, password, decrypted)
		usedV2, _ := received.Load("v2")
		require.Equal(t, len(password) > 190, usedV2, "only a password longer than the RSA limit uses the v2 envelope")
	}
}

func TestMatureProtocolNewAPIAccessRefusalsAreReportedDistinctly(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		check   func(t *testing.T, err error)
		kind    string
		message string
	}{
		{
			name:   "missing scope",
			status: http.StatusForbidden,
			body:   `{"success":false,"message":"The access token lacks the required permission: profile:read","message_key":"The access token lacks the required permission: {{scope}}","message_params":{"scope":"profile:read"},"code":"ACCESS_TOKEN_SCOPE_DENIED"}`,
			check: func(t *testing.T, err error) {
				var scope *providerScopeError
				require.ErrorAs(t, err, &scope)
				require.Equal(t, "profile:read", scope.scope)
				require.NotErrorIs(t, err, errProviderAuthentication, "a missing permission is not a failed login")
			},
			kind: "scope", message: "profile:read",
		},
		{
			name:   "expired token",
			status: http.StatusUnauthorized,
			body:   `{"success":false,"message":"Unauthorized, the access token has expired","message_key":"Unauthorized, the access token has expired","code":"ACCESS_TOKEN_EXPIRED"}`,
			check: func(t *testing.T, err error) {
				require.ErrorIs(t, err, errProviderTokenExpired)
				require.ErrorIs(t, err, errProviderAuthentication)
			},
			kind: "auth", message: "expired",
		},
		{
			name:   "retired legacy token",
			status: http.StatusUnauthorized,
			body:   `{"success":false,"message":"Legacy access tokens have been retired. Create a new access token in security settings","code":"ACCESS_TOKEN_LEGACY_RETIRED"}`,
			check: func(t *testing.T, err error) {
				require.ErrorIs(t, err, errProviderTokenRetired)
			},
			kind: "auth", message: "nap_",
		},
		{
			name:   "legacy refusal with HTTP 200",
			status: http.StatusOK,
			body:   `{"success":false,"message":"无权进行此操作，access token 无效"}`,
			check: func(t *testing.T, err error) {
				require.ErrorIs(t, err, errProviderAuthentication)
			},
			kind: "auth", message: "Provider login failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/user/self" {
					writeProviderJSON(w, tc.status, tc.body)
					return
				}
				writeProviderJSON(w, http.StatusOK, `{"success":true,"message":"","data":{"quota_per_unit":500000}}`)
			}))
			defer server.Close()
			_, err := probeProviderAccount(context.Background(), providerRecord{SiteType: "newapi", AuthMode: "token", BaseURL: server.URL},
				newAPITokenSecret(t, testNewAPIPersonalToken), server.Client())
			require.Error(t, err)
			tc.check(t, err)

			require.Contains(t, providerPublicError(err), tc.message)
		})
	}
}
