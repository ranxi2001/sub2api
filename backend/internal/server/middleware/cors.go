package middleware

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
)

var corsWarningOnce sync.Once

// CORS 跨域中间件
func CORS(cfg config.CORSConfig) gin.HandlerFunc {
	allowedOrigins := normalizeOrigins(cfg.AllowedOrigins)
	allowAll := false
	for _, origin := range allowedOrigins {
		if origin == "*" {
			allowAll = true
			break
		}
	}
	wildcardWithSpecific := allowAll && len(allowedOrigins) > 1
	if wildcardWithSpecific {
		allowedOrigins = []string{"*"}
	}
	allowCredentials := cfg.AllowCredentials

	corsWarningOnce.Do(func() {
		if len(allowedOrigins) == 0 {
			log.Println("Warning: CORS allowed_origins not configured; cross-origin requests will be rejected.")
		}
		if wildcardWithSpecific {
			log.Println("Warning: CORS allowed_origins includes '*'; wildcard will take precedence over explicit origins.")
		}
		if allowAll && allowCredentials {
			log.Println("Warning: CORS allowed_origins set to '*', disabling allow_credentials.")
		}
	})
	if allowAll && allowCredentials {
		allowCredentials = false
	}

	allowedSet := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		if origin == "" || origin == "*" {
			continue
		}
		allowedSet[origin] = struct{}{}
	}
	allowHeaders := []string{
		"Content-Type", "Content-Length", "Accept-Encoding", "X-CSRF-Token", "Authorization",
		"accept", "origin", "Cache-Control", "X-Requested-With", "X-API-Key", "X-Admin-UI-Request", "X-User-UI-Request", "Anthropic-Version", "X-Image-Usage",
	}
	// OpenAI Node SDK 会发送 x-stainless-* 请求头，需在 CORS 中显式放行。
	openAIProperties := []string{
		"lang", "package-version", "os", "arch", "retry-count", "runtime",
		"runtime-version", "async", "helper-method", "poll-helper", "custom-poll-interval", "timeout",
	}
	for _, prop := range openAIProperties {
		allowHeaders = append(allowHeaders, "x-stainless-"+prop)
	}
	allowHeadersValue := strings.Join(allowHeaders, ", ")

	return func(c *gin.Context) {
		// Published Pelican results use explicit API key headers, never cookies. Handle
		// its preflight here because this global middleware precedes routes.
		if isPublicPelicanShowcasePath(c.Request.URL.Path) {
			method := c.Request.Method
			if method == http.MethodOptions {
				method = c.GetHeader("Access-Control-Request-Method")
			}
			if method == http.MethodGet || method == http.MethodHead {
				c.Header("Access-Control-Allow-Origin", "*")
				c.Header("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
				c.Header("Access-Control-Allow-Headers", "Accept, Authorization, X-API-Key, X-Goog-API-Key, If-None-Match, Cache-Control")
				c.Header("Access-Control-Expose-Headers", "ETag, Cache-Control, Retry-After")
				c.Header("Access-Control-Max-Age", "86400")
				if c.Request.Method == http.MethodOptions {
					c.AbortWithStatus(http.StatusNoContent)
					return
				}
				c.Next()
				return
			}
		}
		origin := strings.TrimSpace(c.GetHeader("Origin"))
		originAllowed := allowAll
		if origin != "" && !allowAll {
			_, originAllowed = allowedSet[origin]
		}

		if originAllowed {
			if allowAll {
				c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
			} else if origin != "" {
				c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
				c.Writer.Header().Add("Vary", "Origin")
			}
			if allowCredentials {
				c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			c.Writer.Header().Set("Access-Control-Allow-Headers", allowHeadersValue)
			c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE, PATCH")
			c.Writer.Header().Set("Access-Control-Expose-Headers", "ETag, Server-Timing, X-Client-Request-ID")
			c.Writer.Header().Set("Access-Control-Max-Age", "86400")
		}
		// 处理预检请求
		if c.Request.Method == http.MethodOptions {
			if originAllowed {
				c.AbortWithStatus(http.StatusNoContent)
			} else {
				c.AbortWithStatus(http.StatusForbidden)
			}
			return
		}

		c.Next()
	}
}

func isPublicPelicanShowcasePath(path string) bool {
	const root = "/api/v1/public/pelican-showcase"
	if path == root {
		return true
	}
	id, ok := strings.CutPrefix(path, root+"/items/")
	if !ok {
		return false
	}
	value, err := strconv.ParseInt(id, 10, 64)
	return err == nil && value > 0
}

func normalizeOrigins(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		normalized = append(normalized, trimmed)
	}
	return normalized
}
