package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type credentialHandlerEncryptor struct {
	ready bool
	err   error
}

func (e *credentialHandlerEncryptor) Encrypt(value string) (string, error) { return value, nil }
func (e *credentialHandlerEncryptor) Decrypt(value string) (string, error) { return value, nil }
func (e *credentialHandlerEncryptor) EncryptionStatus() (service.CredentialEncryptionStatus, error) {
	return service.CredentialEncryptionStatus{Configured: e.ready, Source: "local_file"}, e.err
}
func (e *credentialHandlerEncryptor) InitializeEncryption() (service.CredentialEncryptionStatus, error) {
	if e.err == nil {
		e.ready = true
	}
	return e.EncryptionStatus()
}

func TestCredentialEncryptionHandlerStatusAndInitialization(t *testing.T) {
	encryptor := &credentialHandlerEncryptor{}
	reauth := service.NewOpenAIOAuthReauthService(nil, nil, nil, nil, encryptor, false, nil, nil)
	h := NewOpenAIReloginHandler(nil, reauth)
	r := gin.New()
	r.GET("/encryption", h.CredentialEncryption)
	r.POST("/encryption/initialize", h.InitializeCredentialEncryption)
	for _, test := range []struct{ method, path, body string }{
		{"GET", "/encryption", `{"code":0,"message":"success","data":{"configured":false,"source":"local_file"}}`},
		{"POST", "/encryption/initialize", `{"code":0,"message":"success","data":{"configured":true,"source":"local_file"}}`},
		{"GET", "/encryption", `{"code":0,"message":"success","data":{"configured":true,"source":"local_file"}}`},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(test.method, test.path, nil))
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		require.JSONEq(t, test.body, w.Body.String())
	}
}

func TestCredentialEncryptionHandlerBlocksLoginBeforeCreatingJob(t *testing.T) {
	encryptor := &credentialHandlerEncryptor{}
	reauth := service.NewOpenAIOAuthReauthService(nil, nil, nil, nil, encryptor, false, nil, nil)
	// A nil login service would panic if the handler began a login job.
	h := NewOpenAIReloginHandler(nil, reauth)
	r := gin.New()
	r.POST("/login", h.StartTwoFALogin)
	for _, storageErr := range []error{nil, errors.New("private-storage-detail")} {
		encryptor.err = storageErr
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/login", strings.NewReader(`{"email":"user@example.com","password":"fixture-password","mfa_secret":"fixture-totp","credential_target":"operations"}`))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if storageErr == nil {
			require.Equal(t, http.StatusBadRequest, w.Code)
		} else {
			require.Equal(t, http.StatusServiceUnavailable, w.Code)
		}
		for _, secret := range []string{"fixture-password", "fixture-totp", "private-storage-detail"} {
			require.NotContains(t, w.Body.String(), secret)
		}
	}
}
