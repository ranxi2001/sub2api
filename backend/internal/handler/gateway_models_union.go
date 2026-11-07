package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *GatewayHandler) multiGroupModels(c *gin.Context, groups []*service.APIKey) {
	// Copy before sorting: the authorization context and cached auth objects are
	// read-only. Lowest group ID wins duplicate metadata, independently of price.
	groups = append([]*service.APIKey(nil), groups...)
	sort.Slice(groups, func(i, j int) bool { return groups[i].Group.ID < groups[j].Group.ID })
	entries := make(map[string]json.RawMessage)
	for _, key := range groups {
		catalog, err := h.groupModelCatalog(c, key)
		if err != nil {
			writeOpenAIModelsError(c, http.StatusServiceUnavailable, "api_error", "Model discovery is temporarily unavailable")
			return
		}
		for _, raw := range catalog {
			var entry struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw, &entry) != nil || strings.TrimSpace(entry.ID) == "" {
				writeOpenAIModelsError(c, http.StatusBadGateway, "upstream_error", "Invalid model catalogue entry")
				return
			}
			if !service.UserGroupDeniesModel(key.DeniedModelsInGroup(), entry.ID) {
				if _, exists := entries[entry.ID]; !exists {
					entries[entry.ID] = raw
				}
			}
		}
	}
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	models := make([]json.RawMessage, 0, len(ids))
	for _, id := range ids {
		models = append(models, entries[id])
	}
	// Per-group deny filtering has already run. Applying the representative
	// group's deny list here would hide models authorized through another group.
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": models})
}

func (h *GatewayHandler) groupModelCatalog(c *gin.Context, key *service.APIKey) ([]json.RawMessage, error) {
	group := key.Group
	platform := group.Platform
	if forced, ok := middleware.GetForcePlatformFromContext(c); ok && strings.TrimSpace(forced) != "" {
		platform = forced
	}
	if platform == service.PlatformOpenAI && group.Platform == service.PlatformOpenAI && group.CodexModelsManifestConfig.Enabled {
		if h.openAIGatewayService == nil {
			return nil, fmt.Errorf("pinned model discovery is unavailable")
		}
		response, _, err := h.openAIGatewayService.FetchPinnedOpenAIModelsList(c.Request.Context(), group, h.maxAccountSwitches, "")
		if err != nil {
			return nil, err
		}
		var catalog struct {
			Data []json.RawMessage `json:"data"`
		}
		if response == nil || json.Unmarshal(response.Body, &catalog) != nil || catalog.Data == nil {
			return nil, fmt.Errorf("invalid pinned model catalogue")
		}
		return catalog.Data, nil
	}
	ids, err := h.gatewayService.GetGroupModelCatalog(c.Request.Context(), group.ID, platform)
	if err != nil {
		return nil, err
	}
	if group.ModelAllowlistEnabled() {
		ids = group.ModelAllowlist.FilterForListing(ids)
	}
	body, err := json.Marshal(modelsListEntries(platform, ids))
	if err != nil {
		return nil, err
	}
	var entries []json.RawMessage
	err = json.Unmarshal(body, &entries)
	return entries, err
}
