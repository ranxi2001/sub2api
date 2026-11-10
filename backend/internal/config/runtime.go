package config

import (
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/serverless"
	"time"
)

const (
	RuntimeRoleFull    = "full"
	RuntimeRoleGateway = "gateway"
)

// RuntimeConfig separates the primary's global jobs from request replicas.
// Empty remains equivalent to full for programmatically constructed configs.
type RuntimeConfig struct {
	NodeID                  string                   `mapstructure:"node_id"`
	ImageRelayOwnerOrigin   string                   `mapstructure:"image_relay_owner_origin"`
	WSNodeOwner             WSNodeOwnerConfig        `mapstructure:"ws_node_owner"`
	SchedulerReadiness      SchedulerReadinessConfig `mapstructure:"scheduler_readiness"`
	SchedulerSnapshotMode   string                   `mapstructure:"scheduler_snapshot_mode"`
	RegionalFundsFile       string                   `mapstructure:"regional_funds_file"`
	RegionalConcurrencyFile string                   `mapstructure:"regional_concurrency_file"`
	RegionalAuthFile        string                   `mapstructure:"regional_auth_file"`
	RegionalSchedulerFile   string                   `mapstructure:"regional_scheduler_file"`
	Role                    string                   `mapstructure:"role"`
	ServerlessID            string                   `mapstructure:"serverless_id"`
	ServerlessEndpoint      string                   `mapstructure:"serverless_endpoint"`
	ServerlessRegion        string                   `mapstructure:"serverless_region"`
	ServerlessSecret        string                   `mapstructure:"serverless_secret"`
	// Zero leaves the response-header budget to the request context and Pod.
	ServerlessResponseHeaderTimeout int `mapstructure:"serverless_response_header_timeout"`
}

func (c *Config) RunsBackgroundJobs() bool {
	return c == nil || c.Runtime.Role != RuntimeRoleGateway
}

func (c *Config) validateRuntime() error {
	if c.Runtime.NodeID != "" {
		if err := (WSNodeOwnerConfig{Enabled: true, NodeID: c.Runtime.NodeID}).Validate(); err != nil {
			return fmt.Errorf("runtime.node_id requires 1 to 64 lowercase identifier characters")
		}
		if c.Runtime.WSNodeOwner.Enabled && c.Runtime.WSNodeOwner.NodeID != c.Runtime.NodeID {
			return fmt.Errorf("runtime.node_id must match the enabled WS node owner identity")
		}
	}
	if err := ValidateImageRelayOwnerOrigin(c.Runtime.ImageRelayOwnerOrigin); err != nil {
		return err
	}
	if err := c.Runtime.WSNodeOwner.Validate(); err != nil {
		return err
	}
	if err := c.validateSchedulerReadiness(); err != nil {
		return err
	}
	if err := c.validateSchedulerSnapshotMode(); err != nil {
		return err
	}
	if c.Runtime.ServerlessResponseHeaderTimeout < 0 || c.Runtime.ServerlessResponseHeaderTimeout > 86400 {
		return fmt.Errorf("runtime.serverless_response_header_timeout must be between 0 and 86400 seconds")
	}
	if err := serverless.ValidateRuntime(serverless.Runtime{
		ID: c.Runtime.ServerlessID, Endpoint: c.Runtime.ServerlessEndpoint,
		Region: c.Runtime.ServerlessRegion, Secret: c.Runtime.ServerlessSecret,
		Gateway:               c.Runtime.Role == RuntimeRoleGateway,
		ResponseHeaderTimeout: time.Duration(c.Runtime.ServerlessResponseHeaderTimeout) * time.Second,
	}); err != nil {
		return err
	}
	switch c.Runtime.Role {
	case "", RuntimeRoleFull, RuntimeRoleGateway:
	default:
		return fmt.Errorf("runtime.role must be full or gateway")
	}
	if c.Server.GracefulShutdownTimeout < 0 || c.Server.GracefulShutdownTimeout > 3600 {
		return fmt.Errorf("server.graceful_shutdown_timeout must be between 0 and 3600 seconds")
	}
	if c.Server.ShutdownDrainDelay < 0 || c.Server.ShutdownDrainDelay > 300 {
		return fmt.Errorf("server.shutdown_drain_delay must be between 0 and 300 seconds")
	}
	return nil
}

func (c ServerConfig) ShutdownTimeout() time.Duration {
	if c.GracefulShutdownTimeout <= 0 {
		return 5 * time.Second
	}
	return time.Duration(c.GracefulShutdownTimeout) * time.Second
}
