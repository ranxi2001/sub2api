// Package server provides HTTP server initialization and configuration.
package server

import (
	"context"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/serverless"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/iprisk"
	"github.com/Wei-Shaw/sub2api/internal/pkg/websearch"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/setup"

	"github.com/gin-gonic/gin"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
	"golang.org/x/net/http2"
)

// ProviderSet 提供服务器层的依赖
var ProviderSet = wire.NewSet(
	ProvideLifecycle,
	ProvideRouter,
	ProvideHTTPServer,
)

// ProvideRouter 提供路由器
func ProvideRouter(
	cfg *config.Config,
	handlers *handler.Handlers,
	jwtAuth middleware2.JWTAuthMiddleware,
	optionalJWTAuth middleware2.OptionalJWTAuthMiddleware,
	adminAuth middleware2.AdminAuthMiddleware,
	apiKeyAuth middleware2.APIKeyAuthMiddleware,
	auditLog middleware2.AuditLogMiddleware,
	stepUpAuth middleware2.StepUpAuthMiddleware,
	apiKeyService *service.APIKeyService,
	subscriptionService *service.SubscriptionService,
	opsService *service.OpsService,
	settingService *service.SettingService,
	compositeResolver *service.CompositeRouteResolver,
	redisClient *redis.Client,
	lifecycle *Lifecycle,
) *gin.Engine {
	if cfg.Server.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(middleware2.Recovery())
	configureTrustedProxies(r, cfg.Server)

	// Wire up websearch Manager builder so it initializes on startup and rebuilds on config save.
	settingService.SetWebSearchManagerBuilder(context.Background(), func(cfg *service.WebSearchEmulationConfig, proxyURLs map[int64]string) {
		if cfg == nil || !cfg.Enabled || len(cfg.Providers) == 0 {
			service.SetWebSearchManager(nil)
			return
		}
		configs := make([]websearch.ProviderConfig, 0, len(cfg.Providers))
		for _, p := range cfg.Providers {
			if p.APIKey == "" {
				continue
			}
			pc := websearch.ProviderConfig{
				Type:       p.Type,
				APIKey:     p.APIKey,
				QuotaLimit: derefInt64(p.QuotaLimit),
				ExpiresAt:  p.ExpiresAt,
			}
			if p.SubscribedAt != nil {
				pc.SubscribedAt = p.SubscribedAt
			}
			if p.ProxyID != nil {
				pc.ProxyID = *p.ProxyID
				if u, ok := proxyURLs[*p.ProxyID]; ok {
					pc.ProxyURL = u
				} else {
					// Proxy configured but not found — skip this provider to prevent direct connection.
					slog.Warn("websearch: proxy not found for provider, skipping",
						"provider", p.Type, "proxy_id", *p.ProxyID)
					continue
				}
			}
			configs = append(configs, pc)
		}
		service.SetWebSearchManager(websearch.NewManager(configs, redisClient))
	})

	manager, err := serverless.New(serverless.Runtime{
		ID: cfg.Runtime.ServerlessID, Endpoint: cfg.Runtime.ServerlessEndpoint,
		Region: cfg.Runtime.ServerlessRegion, Secret: cfg.Runtime.ServerlessSecret,
		Gateway: cfg.Runtime.Role == config.RuntimeRoleGateway, Version: settingService.ServerlessVersion(),
		ResponseHeaderTimeout: time.Duration(cfg.Runtime.ServerlessResponseHeaderTimeout) * time.Second,
	}, redisClient, settingService.ServerlessStore())
	if err != nil {
		panic(err)
	}
	settingService.Serverless = manager
	settingService.IPRisk = iprisk.New(settingService.IPRiskStore(), redisClient, serverless.NewGeoJSResolver(redisClient), strings.EqualFold(os.Getenv("IP_RISK_DISABLED"), "true"))
	settingService.IPRisk.Start()
	if cfg.Runtime.Role != config.RuntimeRoleGateway && handlers.Admin != nil && handlers.Admin.Setting != nil {
		handlers.Admin.Setting.SetKeyRiskAPIKeys(apiKeyService)
		stop, err := handlers.Admin.Setting.StartKeyRisk(cfg.Database.DSN())
		if err != nil {
			slog.Warn("key risk initialization failed")
		} else {
			lifecycle.onDrain = append(lifecycle.onDrain, stop)
		}
	}
	if handlers.Admin != nil && handlers.Admin.Setting != nil {
		if handlers.Admin.PromptAudit != nil {
			handlers.Admin.Setting.ConfigureIPAudit(handlers.Admin.AuditLog, handlers.Admin.PromptAudit.IPAuditPenaltySummary, handlers.Admin.PromptAudit.ReleaseIPAuditPenalty)
		}
	}
	lifecycle.onDrain = append(lifecycle.onDrain, settingService.IPRisk.Stop)
	manager.Start(func(ctx context.Context) error {
		if lifecycle.isDraining() {
			return fmt.Errorf("draining")
		}
		return lifecycle.checkWithinBudget(ctx)
	})
	lifecycle.onDrain = append(lifecycle.onDrain, manager.Stop)
	r.Use(manager.Ingress())
	if handlers.Admin != nil && handlers.Admin.Usage != nil {
		stop, err := handlers.Admin.Usage.ConfigureTrafficDelivery(func() string {
			return filepath.Join(setup.GetDataDir(), "user-traffic-pending")
		})
		if err != nil {
			log.Printf("Traffic statistics disabled: %v", err)
		} else {
			lifecycle.onDrain = append(lifecycle.onDrain, stop)
		}
	}
	r.GET("/internal/serverless/probe", manager.ProbeHandler)
	return SetupRouter(r, handlers, jwtAuth, optionalJWTAuth, adminAuth, apiKeyAuth, auditLog, stepUpAuth, apiKeyService, subscriptionService, opsService, settingService, compositeResolver, cfg, redisClient)
}

func configureTrustedProxies(r *gin.Engine, cfg config.ServerConfig) {
	proxies := cfg.EffectiveTrustedProxies()
	if err := r.SetTrustedProxies(proxies); err != nil {
		log.Printf("Failed to set trusted proxies: %v", err)
		_ = r.SetTrustedProxies(nil)
		warnAuthProxyMisconfiguration(r)
		return
	}
	if len(proxies) == 0 && cfg.Mode == "release" {
		log.Printf("Warning: server.trusted_proxies is explicitly empty; auth IP guards use direct peer addresses, shared proxies will share signup quotas and login bans")
	}
	if !cfg.TrustedProxiesConfigured && len(cfg.TrustedProxies) == 0 && cfg.Mode == "release" {
		log.Printf("server.trusted_proxies is not configured; trusting only local proxies at 127.0.0.1 and ::1; Docker bridges and remote proxies require explicit configuration")
	}
	if len(proxies) == 0 || !cfg.TrustedProxiesConfigured {
		warnAuthProxyMisconfiguration(r)
	}
}

func warnAuthProxyMisconfiguration(r *gin.Engine) {
	var once sync.Once
	r.Use(func(c *gin.Context) {
		if c.GetHeader("X-Forwarded-For") != "" || c.GetHeader("X-Real-IP") != "" || c.GetHeader("CF-Connecting-IP") != "" {
			if c.ClientIP() == c.RemoteIP() {
				once.Do(func() {
					slog.Warn("Forwarded client IP headers did not resolve beyond the TCP peer; auth IP guards may share signup quotas and login bans across users; verify server.trusted_proxies and proxy header rewriting before enabling public signup")
				})
			}
		}
		c.Next()
	})
}

// ProvideHTTPServer 提供 HTTP 服务器
func ProvideHTTPServer(cfg *config.Config, router *gin.Engine, lifecycle *Lifecycle) *http.Server {
	httpHandler := http.Handler(router)
	server := &http.Server{
		Addr:           cfg.Server.Address(),
		Handler:        httpHandler,
		MaxHeaderBytes: cfg.Server.MaxHeaderBytes,
		// ReadHeaderTimeout: 读取请求头的超时时间，防止慢速请求头攻击
		ReadHeaderTimeout: time.Duration(cfg.Server.ReadHeaderTimeout) * time.Second,
		// IdleTimeout: 空闲连接超时时间，释放不活跃的连接资源
		IdleTimeout: time.Duration(cfg.Server.IdleTimeout) * time.Second,
		// 注意：不设置 WriteTimeout，因为流式响应可能持续十几分钟
		// 不设置 ReadTimeout，因为大请求体可能需要较长时间读取
	}

	globalMaxSize := cfg.Server.MaxRequestBodySize
	if globalMaxSize <= 0 {
		globalMaxSize = cfg.Gateway.MaxBodySize
	}
	if globalMaxSize > 0 {
		httpHandler = http.MaxBytesHandler(httpHandler, globalMaxSize)
		log.Printf("Global max request body size: %d bytes (%.2f MB)", globalMaxSize, float64(globalMaxSize)/(1<<20))
	}

	// 根据配置决定是否启用 H2C
	if cfg.Server.H2C.Enabled {
		h2cConfig := cfg.Server.H2C
		if err := http2.ConfigureServer(server, &http2.Server{
			MaxConcurrentStreams:         h2cConfig.MaxConcurrentStreams,
			IdleTimeout:                  time.Duration(h2cConfig.IdleTimeout) * time.Second,
			MaxReadFrameSize:             uint32(h2cConfig.MaxReadFrameSize),
			MaxUploadBufferPerConnection: int32(h2cConfig.MaxUploadBufferPerConnection),
			MaxUploadBufferPerStream:     int32(h2cConfig.MaxUploadBufferPerStream),
		}); err != nil {
			log.Printf("Failed to configure HTTP/2 Cleartext (h2c): %v", err)
		} else {
			protocols := new(http.Protocols)
			protocols.SetHTTP1(true)
			protocols.SetUnencryptedHTTP2(true)
			server.Protocols = protocols
			log.Printf("HTTP/2 Cleartext (h2c) enabled: max_concurrent_streams=%d, idle_timeout=%ds, max_read_frame_size=%d, max_upload_buffer_per_connection=%d, max_upload_buffer_per_stream=%d",
				h2cConfig.MaxConcurrentStreams,
				h2cConfig.IdleTimeout,
				h2cConfig.MaxReadFrameSize,
				h2cConfig.MaxUploadBufferPerConnection,
				h2cConfig.MaxUploadBufferPerStream,
			)
		}
	}

	server.Handler = lifecycle.Wrap(httpHandler)
	return server
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
