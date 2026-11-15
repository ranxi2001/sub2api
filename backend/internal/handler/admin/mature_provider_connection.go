package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type providerMetrics = service.ProviderMetrics
type providerRecord struct {
	ID, Name, SiteType, BaseURL, AuthMode, Account, UserID, Status, ErrorKind, SecretEncrypted string
	UpdatedAt                                                                                  *time.Time
}

func ProvideMatureChannelHandler(channels *service.ChannelService, billing *service.BillingService, pricing *service.PricingService, encryptor service.SecretEncryptor, cfg *config.Config) *ChannelHandler {
	h := NewChannelHandler(channels, billing, pricing)
	if cfg != nil && cfg.Totp.EncryptionKeyConfigured {
		h.SetMatureProviderDependencies(encryptor, nil)
	}
	if cfg != nil && cfg.RunsBackgroundJobs() {
		h.startMatureProviderRefresh()
	}
	return h
}

func (h *ChannelHandler) SetMatureProviderDependencies(encryptor service.SecretEncryptor, client *http.Client) {
	h.providerEncryptor, h.providerClient = encryptor, client
}

func (h *ChannelHandler) encryptMatureProviderInput(in *service.MatureProviderInput) error {
	if in.AuthMode == "password" && (in.AccessToken != "" || in.RefreshToken != "") {
		return service.ErrMatureProviderValidation
	}
	if in.AuthMode == "token" && in.Password != "" {
		return service.ErrMatureProviderValidation
	}
	secret := ""
	if in.Password != "" {
		if len(in.Password) > 1024 {
			return service.ErrMatureProviderValidation
		}
		secret = in.Password
	} else if in.AccessToken != "" || in.RefreshToken != "" {
		var err error
		secret, err = encodeProviderToken(in.AccessToken, in.RefreshToken, in.TokenType)
		if err != nil {
			return service.ErrMatureProviderValidation
		}
		if in.SiteType == "newapi" && !newAPIPersonalToken(in.AccessToken) && in.UserID == "" {
			return service.ErrMatureProviderValidation
		}
	}
	if secret == "" {
		return nil
	}
	if h.providerEncryptor == nil {
		return service.ErrMatureProviderValidation
	}
	cipher, err := h.providerEncryptor.Encrypt(secret)
	if err != nil {
		return service.ErrMatureProviderValidation
	}
	in.CredentialCipher = &cipher
	return nil
}

func (h *ChannelHandler) matureProviderClient() (*http.Client, error) {
	if h.providerClient == nil {
		return providerHTTPClient()
	}
	client := *h.providerClient
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client, nil
}

func providerProtocolRecord(p *service.MatureProvider) providerRecord {
	return providerRecord{ID: strconv.FormatInt(p.ID, 10), Name: p.Name, SiteType: p.SiteType, BaseURL: p.BaseURL, AuthMode: p.AuthMode, Account: p.Account, UserID: p.UserID, Status: p.Status}
}

func (h *ChannelHandler) RefreshMatureProvider(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
	var in struct {
		Revision int64 `json:"revision"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 || decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF || in.Revision < 1 {
		matureProviderError(c, service.ErrMatureProviderValidation)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	out, err := h.refreshMatureProvider(ctx, id, in.Revision)
	if err != nil {
		matureProviderError(c, err)
		return
	}
	response.Success(c, out)
}

func (h *ChannelHandler) refreshMatureProvider(ctx context.Context, id, revision int64) (*service.MatureProvider, error) {
	r, err := h.channelService.MatureProviderRepository()
	if err != nil {
		return nil, err
	}
	return r.WithMatureProviderSession(ctx, id, revision, func(operationCtx context.Context, p *service.MatureProvider, cipher string) (service.ProviderSessionUpdate, error) {
		u := service.ProviderSessionUpdate{}
		if h.providerEncryptor == nil || cipher == "" {
			return u, service.ErrMatureProviderValidation
		}
		secret, e := h.providerEncryptor.Decrypt(cipher)
		if e != nil {
			return u, service.ErrMatureProviderValidation
		}
		client, e := h.matureProviderClient()
		if e != nil {
			return u, e
		}
		defer client.CloseIdleConnections()
		probe, e := probeProviderAccount(operationCtx, providerProtocolRecord(p), secret, client)
		if probe.secret != "" {
			renewed, encryptErr := h.providerEncryptor.Encrypt(probe.secret)
			if encryptErr != nil {
				return u, encryptErr
			}
			u.Cipher = &renewed
		}
		if e != nil {
			u.Status = "disconnected"
			u.LastError = providerPublicError(e)
		} else {
			u.Status = "connected"
			u.Metrics = &probe.metrics
		}
		return u, nil
	})
}
