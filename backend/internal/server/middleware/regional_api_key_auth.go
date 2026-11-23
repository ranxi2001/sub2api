package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// RegionalAPIKeyAuth runs only identity/permission checks using a local replica.
// It performs no last-used writes, subscription refresh, Redis admission, billing
// or ops persistence. A separate unconditional gate still blocks model serving.
type regionalAPIKeyAuthenticator interface {
	GetRegionalByKey(context.Context, string) (*service.APIKey, error)
}

func RegionalAPIKeyAuth(svc regionalAPIKeyAuthenticator, cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request != nil && c.Request.URL != nil && c.Request.URL.Path == "/v1/usage/records" {
			AbortWithError(c, http.StatusServiceUnavailable, "REGIONAL_USAGE_RECORDS_UNAVAILABLE", "Usage records are available on the primary site. Please use the primary site address.")
			return
		}
		if isKeyGroupHealthRequest(c) {
			AbortWithError(c, http.StatusServiceUnavailable, "REGIONAL_GROUP_HEALTH_UNAVAILABLE", "Query group health on the primary site")
			return
		}
		if apiKeyHeadersTooLarge(c) {
			AbortWithError(c, http.StatusUnauthorized, "INVALID_API_KEY", "Invalid API key")
			return
		}
		// Never accept credentials in URLs, including native Gemini preview routes.
		if c.Query("key") != "" || c.Query("api_key") != "" {
			AbortWithError(c, http.StatusBadRequest, "api_key_in_query_deprecated", "Use an API key header")
			return
		}
		var key string
		if parts := strings.SplitN(c.GetHeader("Authorization"), " ", 2); len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			key = strings.TrimSpace(parts[1])
		}
		if key == "" {
			key = c.GetHeader("x-api-key")
		}
		if key == "" {
			key = c.GetHeader("x-goog-api-key")
		}
		if key == "" || len(key) > service.MaxAPIKeyCredentialBytes {
			AbortWithError(c, http.StatusUnauthorized, "INVALID_API_KEY", "Invalid API key")
			return
		}
		auth, err := svc.GetRegionalByKey(c.Request.Context(), key)
		if err != nil {
			if errors.Is(err, service.ErrAPIKeyNotFound) {
				AbortWithError(c, http.StatusUnauthorized, "INVALID_API_KEY", "Invalid API key")
			} else {
				AbortWithError(c, http.StatusServiceUnavailable, "REGIONAL_AUTH_UNAVAILABLE", "Regional authorization unavailable")
			}
			return
		}
		if len(auth.ConfiguredGroupIDs()) > 1 {
			AbortWithError(c, http.StatusServiceUnavailable, "MULTI_GROUP_ROUTING_UNAVAILABLE", "Multi-group routing is unavailable on this regional gateway")
			return
		}
		if len(auth.IPWhitelist) > 0 || len(auth.IPBlacklist) > 0 {
			allowed, _ := ip.CheckIPRestrictionWithCompiledRules(ip.GetSecurityClientIP(c, cfg.TrustForwardedIPForAPIKeyACL()), auth.CompiledIPWhitelist, auth.CompiledIPBlacklist)
			if !allowed {
				AbortWithError(c, http.StatusForbidden, "ACCESS_DENIED", "Access denied")
				return
			}
		}
		c.Set(string(ContextKeyAPIKey), auth)
		c.Set(string(ContextKeyUser), AuthSubject{UserID: auth.User.ID, Concurrency: auth.User.Concurrency})
		c.Set(string(ContextKeyUserRole), auth.User.Role)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.UserID, auth.User.ID))
		setGroupContext(c, auth.Group)
		nextWithAPIKeyAdmissionLookup(c, svc.GetRegionalByKey, key, ip.GetSecurityClientIP(c, cfg.TrustForwardedIPForAPIKeyACL()), auth, strings.HasPrefix(c.Request.URL.Path, "/v1beta/"))
	}
}
