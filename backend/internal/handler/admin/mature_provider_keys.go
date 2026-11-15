package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) SetMatureProviderCatalog(catalog *ChannelHandler) {
	h.providerCatalog = catalog
}

func (h *ChannelHandler) withMatureProviderSession(ctx context.Context, id, revision int64, run func(context.Context, *providerSession) error) error {
	r, err := h.channelService.MatureProviderRepository()
	if err != nil {
		return err
	}
	_, err = r.WithMatureProviderSession(ctx, id, revision, func(operationCtx context.Context, p *service.MatureProvider, cipher string) (service.ProviderSessionUpdate, error) {
		u := service.ProviderSessionUpdate{}
		if h.providerEncryptor == nil || cipher == "" {
			return u, errProviderStoredLogin
		}
		secret, e := h.providerEncryptor.Decrypt(cipher)
		if e != nil {
			return u, errProviderStoredLogin
		}
		client, e := h.matureProviderClient()
		if e != nil {
			return u, e
		}
		defer client.CloseIdleConnections()
		session := &providerSession{record: providerProtocolRecord(p), secret: secret, remote: providerRemote{client: client, base: p.BaseURL}}
		e = session.authenticate(operationCtx)
		if e == nil {
			e = run(operationCtx, session)
		}
		if session.rotated != "" {
			rotated, encryptErr := h.providerEncryptor.Encrypt(session.rotated)
			if encryptErr != nil {
				return u, encryptErr
			}
			u.Cipher = &rotated
		}
		return u, e
	})
	return err
}

func (h *ChannelHandler) MatureProviderOptions(c *gin.Context) { h.ListMatureProviders(c) }

func (h *ChannelHandler) MatureProviderKeys(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		matureProviderError(c, service.ErrMatureProviderValidation)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	keys := []providerKeyView{}
	err = h.withMatureProviderSession(ctx, id, 0, func(ctx context.Context, s *providerSession) error { var e error; keys, e = s.listKeys(ctx); return e })
	if err != nil {
		response.ErrorFrom(c, providerKeyError(err))
		return
	}
	response.Success(c, gin.H{"provider_id": id, "keys": keys, "synced_at": time.Now().UTC()})
}

func (h *ChannelHandler) MatureProviderGroups(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		matureProviderError(c, service.ErrMatureProviderValidation)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	groups := []providerGroupView{}
	err = h.withMatureProviderSession(ctx, id, 0, func(ctx context.Context, s *providerSession) error {
		var e error
		groups, e = s.listGroups(ctx)
		return e
	})
	if err != nil {
		response.ErrorFrom(c, providerKeyError(err))
		return
	}
	response.Success(c, gin.H{"provider_id": id, "groups": groups})
}

func matureHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (h *ChannelHandler) CreateMatureProviderKey(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var in struct {
		GroupID  string `json:"group_id"`
		Revision int64  `json:"revision"`
		Confirm  bool   `json:"confirm"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	requestKey := c.GetHeader("Idempotency-Key")
	if err != nil || id < 1 || decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF || !in.Confirm || in.Revision < 1 || !validProviderGroupID(in.GroupID) || len(requestKey) < 16 || len(requestKey) > 128 || !validProviderCredential(requestKey) {
		matureProviderError(c, service.ErrMatureProviderValidation)
		return
	}
	encoded, _ := json.Marshal(in)
	keyHash, inputHash := matureHash(requestKey), matureHash(string(encoded))
	r, err := h.channelService.MatureProviderRepository()
	if err != nil {
		matureProviderError(c, err)
		return
	}
	reserved, status, body, err := r.ReserveMatureProviderKey(c.Request.Context(), id, keyHash, inputHash)
	if err != nil {
		matureProviderError(c, err)
		return
	}
	if !reserved {
		c.Data(status, "application/json", body)
		return
	}
	middleware.SetAuditAction(c, "admin.provider.create_key")
	middleware.SetAuditExtra(c, map[string]any{"provider_id": id, "operation_hash": keyHash})
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	var created providerCreatedKey
	attempted := false
	err = h.withMatureProviderSession(ctx, id, in.Revision, func(ctx context.Context, s *providerSession) error {
		groups, e := s.listGroups(ctx)
		if e != nil {
			return e
		}
		for _, group := range groups {
			if group.ID == in.GroupID {
				attempted = true
				created, e = s.createKey(ctx, group)
				return e
			}
		}
		return errProviderGroupUnavailable
	})
	status = 200
	var result any = gin.H{"code": 0, "data": gin.H{"provider_id": id, "created": true, "key_id": created.keyID, "name": created.name, "group_id": in.GroupID}}
	if err != nil {
		status = 502
		reason := "PROVIDER_KEY_CREATE_FAILED"
		message := "Provider refused the operation; review the saved configuration"
		var uncertain *providerKeyCreateUncertainError
		if errors.As(err, &uncertain) || (attempted && created.keyID != "") {
			reason = "PROVIDER_KEY_CREATE_UNCERTAIN"
			message = "Creation result is unknown; refresh keys before starting a new operation"
		}
		result = gin.H{"code": 502, "reason": reason, "message": message}
	}
	body, _ = json.Marshal(result)
	// Persist the result even if the requesting browser disconnected. A failed
	// save leaves the durable pending tombstone, which also forbids replay.
	finish, stop := context.WithTimeout(context.Background(), 8*time.Second)
	defer stop()
	if err = r.CompleteMatureProviderKey(finish, id, keyHash, inputHash, status, body); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"code": http.StatusBadGateway, "reason": "PROVIDER_KEY_CREATE_UNCERTAIN", "message": "The result could not be saved; inspect upstream keys before starting a new operation"})
		return
	}
	c.Data(status, "application/json", body)
}

func (h *ChannelHandler) resolveMatureProviderKey(ctx context.Context, id int64, keyID, platform string) (map[string]any, map[string]any, error) {
	if id < 1 || !validProviderKeyID(keyID) {
		return nil, nil, service.ErrMatureProviderValidation
	}
	var credentials, provenance map[string]any
	err := h.withMatureProviderSession(ctx, id, 0, func(ctx context.Context, s *providerSession) error {
		key, e := s.resolveKey(ctx, keyID)
		if e != nil {
			return e
		}
		base := strings.TrimRight(s.record.BaseURL, "/")
		if platform == service.PlatformGrok {
			base += "/v1"
		}
		credentials = map[string]any{"api_key": key.secret, "base_url": base}
		provenance = map[string]any{"provider_id": id, "provider_name": s.record.Name, "site_type": s.record.SiteType, "key_id": key.ID, "key_name": key.Name, "group_id": key.GroupID, "group_name": key.GroupName, "group_multiplier": key.GroupMultiplier}
		return nil
	})
	return credentials, provenance, err
}
