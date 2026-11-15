//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// This provider writes a key marker in the caller's transaction so a failed
// multi-module save also verifies that provisioning is rolled back atomically.
type setupImageSiteFake struct {
	db           *sql.DB
	nextID       int64
	revoked      []int64
	imageBaseURL string
}

func (p *setupImageSiteFake) Options(context.Context, int64, Config) (SiteAIOptions, error) {
	return SiteAIOptions{}, nil
}

func (p *setupImageSiteFake) prepare(ctx context.Context, tx *sql.Tx, owner, group, oldID, oldOwner, oldGroup int64, oldMode, oldKey, base, purpose string) (SiteAIConnection, error) {
	if oldMode == "site" && oldGroup == group && oldID > 0 {
		return SiteAIConnection{BaseURL: base, Key: oldKey, KeyID: oldID, OwnerID: oldOwner, GroupID: group}, nil
	}
	p.nextID++
	_, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES($1,$2)`, fmt.Sprintf("fake-site-key-%d", p.nextID), purpose)
	if err != nil {
		return SiteAIConnection{}, err
	}
	return SiteAIConnection{BaseURL: base, Key: fmt.Sprintf("%s-secret-%d", purpose, p.nextID), KeyID: p.nextID, OwnerID: owner, GroupID: group, Created: true}, nil
}

func (p *setupImageSiteFake) Prepare(ctx context.Context, tx *sql.Tx, owner, group int64, _ float64, old Config) (SiteAIConnection, error) {
	return p.prepare(ctx, tx, owner, group, old.SiteAPIKeyID, old.SiteOwnerID, old.SiteGroupID, old.AIConnection, old.APIKey, "http://127.0.0.1:8080/v1", "chat")
}

func (p *setupImageSiteFake) PrepareImage(ctx context.Context, tx *sql.Tx, owner, group int64, _ float64, old Config) (SiteAIConnection, error) {
	base := p.imageBaseURL
	if base == "" {
		base = "http://127.0.0.1:8080/v1"
	}
	return p.prepare(ctx, tx, owner, group, old.ImageSiteAPIKeyID, old.ImageSiteOwnerID, old.ImageSiteGroupID, old.ImageAIConnection, old.ImageAPIKey, base, "image")
}

func (p *setupImageSiteFake) Revoke(ctx context.Context, id, _ int64) error {
	p.revoked = append(p.revoked, id)
	_, err := p.db.ExecContext(ctx, `DELETE FROM settings WHERE key=$1`, fmt.Sprintf("fake-site-key-%d", id))
	return err
}

func TestSetupImageManagedConnectionIndependentLifecycle(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	p := &setupImageSiteFake{db: db, nextID: 100}
	s.SetSiteAIProvider(p)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	req := setupRequest(c, "chat", "images")
	req.Config.AIConnection, req.Config.SiteGroupID = "site", 2
	req.Config.ImageAIConnection, req.Config.ImageSiteGroupID = "site", 3
	req.Config.ImageModel = "gpt-image-custom"
	req.ImageSiteQuota = 10
	saved, err := s.SaveSetup(ctx, 7, req)
	require.NoError(t, err)
	require.Empty(t, saved.APIKey)
	require.Empty(t, saved.ImageAPIKey)
	require.True(t, saved.ImageAPIKeySet)
	require.NotZero(t, saved.ImageSiteAPIKeyID)
	require.NotEqual(t, saved.SiteAPIKeyID, saved.ImageSiteAPIKeyID)
	c, err = s.Config(ctx)
	require.NoError(t, err)
	chatID, imageID := c.SiteAPIKeyID, c.ImageSiteAPIKeyID
	chatKey := c.APIKey
	var raw string
	require.NoError(t, db.QueryRow(`SELECT value FROM settings WHERE key=$1`, configKey).Scan(&raw))
	require.Contains(t, raw, "encrypted:"+c.ImageAPIKey)

	req = setupRequest(c, "images")
	req.ImageSiteQuota = 10
	_, err = s.SaveSetup(ctx, 7, req)
	require.NoError(t, err)
	require.Equal(t, imageID, p.nextID, "resaving must reuse the image key")
	require.Empty(t, p.revoked)

	c, err = s.Config(ctx)
	require.NoError(t, err)
	req = setupRequest(c, "images")
	req.ImageSiteQuota, req.RegenerateImageSiteKey = 10, true
	_, err = s.SaveSetup(ctx, 7, req)
	require.NoError(t, err)
	require.Equal(t, []int64{imageID}, p.revoked)
	c, err = s.Config(ctx)
	require.NoError(t, err)
	require.Equal(t, chatID, c.SiteAPIKeyID)
	require.Equal(t, chatKey, c.APIKey)
	require.NotEqual(t, imageID, c.ImageSiteAPIKeyID)
	imageID = c.ImageSiteAPIKeyID

	// A managed image credential cannot silently become an external credential.
	req = setupRequest(c, "images")
	req.Config.ImageAIConnection = "external"
	req.Config.ImageBaseURL = "https://external.invalid/v1"
	_, err = s.SaveSetup(ctx, 7, req)
	require.Error(t, err)
	require.Len(t, p.revoked, 1)
	current, err := s.Config(ctx)
	require.NoError(t, err)
	require.Equal(t, imageID, current.ImageSiteAPIKeyID)

	req = setupRequest(current, "images")
	req.Config.ImageAIConnection = "reuse_chat"
	_, err = s.SaveSetup(ctx, 7, req)
	require.NoError(t, err)
	c, err = s.Config(ctx)
	require.NoError(t, err)
	require.Empty(t, c.ImageBaseURL)
	require.Empty(t, c.ImageAPIKey)
	require.Zero(t, c.ImageSiteAPIKeyID)
	require.Zero(t, c.ImageSiteGroupID)
	require.Zero(t, c.ImageSiteOwnerID)
	base, key := imageConnection(c)
	require.Equal(t, c.BaseURL, base)
	require.Equal(t, chatKey, key)
	require.Equal(t, []int64{saved.ImageSiteAPIKeyID, imageID}, p.revoked)
	require.Equal(t, chatID, c.SiteAPIKeyID)
	history, err := s.ConfigHistory(ctx)
	require.NoError(t, err)
	historyRaw, err := json.Marshal(history)
	require.NoError(t, err)
	require.NotContains(t, string(historyRaw), "secret-")
	require.NotContains(t, string(historyRaw), "image_site_api_key_id")
}

func TestSetupImageAndChatProvisioningRollBackTogether(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	p := &setupImageSiteFake{db: db, nextID: 200}
	s.SetSiteAIProvider(p)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	req := setupRequest(c, "chat", "images")
	req.Config.AIConnection, req.Config.SiteGroupID = "site", 2
	req.Config.ImageAIConnection, req.Config.ImageSiteGroupID = "site", 3
	req.Config.ImageSize = "invalid-size"
	req.ImageSiteQuota = 10
	_, err = s.SaveSetup(ctx, 7, req)
	require.Error(t, err)
	require.ElementsMatch(t, []int64{201, 202}, p.revoked)
	current, err := s.Config(ctx)
	require.NoError(t, err)
	require.Equal(t, c.APIKey, current.APIKey)
	require.Equal(t, c.ImageAPIKey, current.ImageAPIKey)
	require.Zero(t, current.SiteAPIKeyID)
	require.Zero(t, current.ImageSiteAPIKeyID)
	var markers int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key LIKE 'fake-site-key-%'`).Scan(&markers))
	require.Zero(t, markers)
}

func TestSetupImageLegacyAndExternalConnectionsPreserveTheirSecrets(t *testing.T) {
	ctx := context.Background()
	for _, independentURL := range []string{"", "https://image.example.invalid/v1"} {
		t.Run(independentURL, func(t *testing.T) {
			db := testDB(t)
			s := savedBot(t, db)
			c, err := s.Config(ctx)
			require.NoError(t, err)
			c.ImageBaseURL, c.ImageAPIKey, c.ImageModel = independentURL, "old-image-secret", "old-image-model"
			_, err = s.SaveConfig(ctx, c)
			require.NoError(t, err)
			c, err = s.Config(ctx)
			require.NoError(t, err)
			req := setupRequest(c, "images")
			req.Config.ImageModel = "new-image-model"
			_, err = s.SaveSetup(ctx, 7, req)
			require.NoError(t, err)
			c, err = s.Config(ctx)
			require.NoError(t, err)
			require.Empty(t, c.ImageAIConnection)
			require.Equal(t, "old-image-secret", c.ImageAPIKey)
			base, key := imageConnection(c)
			if independentURL == "" {
				require.Equal(t, c.BaseURL, base)
			} else {
				require.Equal(t, independentURL, base)
			}
			require.Equal(t, "old-image-secret", key)
			// Selecting the explicit external mode preserves this legacy connection.
			req = setupRequest(c, "images")
			req.Config.ImageAIConnection = "external"
			_, err = s.SaveSetup(ctx, 7, req)
			require.NoError(t, err)
			c, err = s.Config(ctx)
			require.NoError(t, err)
			require.Equal(t, "old-image-secret", c.ImageAPIKey)
			require.Equal(t, independentURL, c.ImageBaseURL)
			require.Zero(t, c.ImageSiteAPIKeyID)
			base, key = imageConnection(c)
			if independentURL == "" {
				require.Equal(t, c.BaseURL, base)
			} else {
				require.Equal(t, independentURL, base)
			}
			require.Equal(t, "old-image-secret", key)
		})
	}
}

func TestSetupImageManagedLegacyWritesCannotRedirectCredential(t *testing.T) {
	ctx := context.Background()
	s := savedBot(t, testDB(t))
	p := &setupImageSiteFake{db: s.db, nextID: 300}
	s.SetSiteAIProvider(p)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	req := setupRequest(c, "images")
	req.Config.ImageAIConnection, req.Config.ImageSiteGroupID = "site", 2
	req.ImageSiteQuota = 10
	_, err = s.SaveSetup(ctx, 7, req)
	require.NoError(t, err)
	c, err = s.Config(ctx)
	require.NoError(t, err)
	// Clearing just the source must not turn managed metadata into a legacy
	// connection that can redirect the stored site credential.
	req = setupRequest(c, "images")
	req.Config.ImageAIConnection = ""
	req.Config.ImageBaseURL = "https://attacker.invalid/v1"
	_, err = s.SaveSetup(ctx, 7, req)
	require.Error(t, err)
	for _, mode := range []string{"section", "basic", "full"} {
		t.Run(mode, func(t *testing.T) {
			draft := c.Public()
			draft.ImageBaseURL = "https://attacker.invalid/v1"
			switch mode {
			case "section":
				_, err = s.SaveConfigSection(ctx, "images", sectionVersion(c, "images"), draft)
			case "basic":
				_, err = s.SaveBasicConfig(ctx, draft)
			case "full":
				_, err = s.SaveConfig(ctx, draft)
			}
			require.Error(t, err)
		})
	}
	legacy := c.Public()
	legacy.ImageAIConnection, legacy.ImageSiteAPIKeyID, legacy.ImageSiteGroupID, legacy.ImageSiteOwnerID = "", 0, 0, 0
	legacy.ImageModel = "legacy-custom-image"
	_, err = s.SaveConfigSection(ctx, "images", sectionVersion(c, "images"), legacy)
	require.NoError(t, err)
	current, err := s.Config(ctx)
	require.NoError(t, err)
	require.Equal(t, c.ImageSiteAPIKeyID, current.ImageSiteAPIKeyID)
	require.Equal(t, c.ImageAPIKey, current.ImageAPIKey)
	require.Equal(t, "legacy-custom-image", current.ImageModel)
	require.Empty(t, p.revoked)
}

func TestSetupImageManagedCredentialReachesImageRequest(t *testing.T) {
	ctx := context.Background()
	original := testInputPNG(t)
	var gotPath, gotAuth, gotModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(original)}}})
	}))
	defer server.Close()
	db := testDB(t)
	s := savedBot(t, db)
	p := &setupImageSiteFake{db: db, nextID: 400, imageBaseURL: server.URL + "/v1"}
	s.SetSiteAIProvider(p)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	req := setupRequest(c, "images")
	req.Config.ImageAIConnection, req.Config.ImageSiteGroupID = "site", 2
	req.Config.ImageModel = "selected-site-image-model"
	req.ImageSiteQuota = 10
	_, err = s.SaveSetup(ctx, 7, req)
	require.NoError(t, err)
	c, err = s.Config(ctx)
	require.NoError(t, err)
	result, err := s.generateImage(ctx, c, "a blue bird")
	require.NoError(t, err)
	require.NotEmpty(t, result)
	require.Equal(t, "/v1/images/generations", gotPath)
	require.Equal(t, "Bearer "+c.ImageAPIKey, gotAuth)
	require.NotEqual(t, "Bearer "+c.APIKey, gotAuth)
	require.Equal(t, "selected-site-image-model", gotModel)
}
