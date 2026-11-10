// Package serverless routes authenticated gateway requests to registered Pods.
package serverless

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const SettingKey = "serverless_routing_v1"

const (
	AffinityAPIKey = "api_key"
	AffinityKeyIP  = "api_key_ip"
)

var identifier = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$")
var countryCode = regexp.MustCompile("^[A-Z]{2}$")

type PodPolicy struct {
	ID       string `json:"id"`
	Endpoint string `json:"endpoint"`
	Enabled  bool   `json:"enabled"`
}
type Region struct {
	Country string   `json:"country"`
	PodIDs  []string `json:"pod_ids"`
}
type Config struct {
	Established       bool        `json:"established"`
	Enabled           bool        `json:"enabled"`
	AffinityScope     string      `json:"affinity_scope,omitempty"`
	FallbackToPrimary bool        `json:"fallback_to_primary,omitempty"`
	Pods              []PodPolicy `json:"pods"`
	Regions           []Region    `json:"regions"`
}
type Store interface {
	Load(context.Context) (Config, error)
	Save(context.Context, Config) error
}

// Runtime values come from trusted process configuration, never a public form.
type Runtime struct {
	ID                    string
	Endpoint              string
	Region                string
	Secret                string
	Version               string
	Gateway               bool
	ResponseHeaderTimeout time.Duration
}

func ValidateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.ForceQuery || (u.Path != "" && u.Path != "/") {
		return errors.New("Pod endpoint must be an origin without credentials, path, query or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if u.Scheme != "http" || err != nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
		return errors.New("use HTTPS, or an HTTP private/loopback IP over a trusted network")
	}
	return nil
}
func Validate(c Config) error {
	if c.AffinityScope != "" && c.AffinityScope != AffinityAPIKey && c.AffinityScope != AffinityKeyIP {
		return errors.New("affinity_scope must be api_key or api_key_ip")
	}
	if len(c.Pods) > 128 || len(c.Regions) > 64 {
		return errors.New("maximum 128 Pods and 64 regions")
	}
	pods := map[string]bool{}
	for _, p := range c.Pods {
		if !identifier.MatchString(p.ID) || p.ID == "primary" || pods[p.ID] {
			return errors.New("Pod IDs must be unique alphanumeric identifiers; primary is reserved")
		}
		pods[p.ID] = true
		if err := ValidateEndpoint(p.Endpoint); err != nil {
			return err
		}
	}
	codes := map[string]bool{}
	for _, r := range c.Regions {
		if !countryCode.MatchString(r.Country) || codes[r.Country] {
			return errors.New("region codes must be unique two-letter uppercase country codes")
		}
		codes[r.Country] = true
		if len(r.PodIDs) == 0 {
			return errors.New("each region requires at least one approved Pod")
		}
		seen := map[string]bool{}
		for _, id := range r.PodIDs {
			if !pods[id] || seen[id] {
				return errors.New("region references an unknown or repeated Pod")
			}
			seen[id] = true
		}
	}
	return nil
}
func ValidateRuntime(r Runtime) error {
	if r.ResponseHeaderTimeout < 0 || r.ResponseHeaderTimeout > 24*time.Hour {
		return errors.New("runtime.serverless_response_header_timeout must be between 0 and 86400 seconds")
	}
	if r.Secret != "" && len(r.Secret) < 32 {
		return errors.New("serverless cluster secret must be at least 32 bytes")
	}
	if r.ID == "" {
		return nil
	}
	if !r.Gateway || !identifier.MatchString(r.ID) || r.ID == "primary" || len(r.Secret) < 32 {
		return errors.New("Pod registration requires gateway role, valid ID and cluster secret")
	}
	if r.Region != "" && !countryCode.MatchString(r.Region) {
		return errors.New("Pod region must be a two-letter country code")
	}
	return ValidateEndpoint(strings.TrimRight(r.Endpoint, "/"))
}
