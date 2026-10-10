package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"math"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ErrAccountOpsConfigValidation identifies static, safe input validation failures.
// Storage and cryptographic errors never wrap this sentinel.
var ErrAccountOpsConfigValidation = errors.New("invalid notification configuration")

func accountOpsConfigValidation(message string) error {
	return fmt.Errorf("%w: %s", ErrAccountOpsConfigValidation, message)
}

type AccountOpsWebhook struct {
	ID                                string  `json:"id"`
	Name                              *string `json:"name,omitempty"`
	Provider                          string  `json:"provider"`
	Enabled                           bool    `json:"enabled"`
	URLConfigured                     bool    `json:"url_configured"`
	SecretConfigured                  bool    `json:"secret_configured"`
	URL                               string  `json:"url,omitempty"`
	Secret                            string  `json:"secret,omitempty"`
	ClearSecret                       bool    `json:"clear_secret,omitempty"`
	MessageTemplate                   *string `json:"message_template,omitempty"`
	urlCipher, secretCipher, revision string
}
type AccountOpsBalanceRule struct {
	NotifyAlert    *bool   `json:"notify_alert"`
	NotifyRecovery *bool   `json:"notify_recovery"`
	AccountID      int64   `json:"account_id"`
	Enabled        bool    `json:"enabled"`
	Threshold      float64 `json:"threshold"`
	Unit           string  `json:"unit"`
}
type AccountOpsQuotaRule struct {
	NotifyAlert      *bool   `json:"notify_alert"`
	NotifyRecovery   *bool   `json:"notify_recovery"`
	AccountID        int64   `json:"account_id"`
	Enabled          bool    `json:"enabled"`
	ThresholdPercent float64 `json:"threshold_percent"`
	Window           string  `json:"window"`
}
type AccountOpsDetails struct {
	UsedPercent      *float64   `json:"used_percent,omitempty"`
	ThresholdPercent *float64   `json:"threshold_percent,omitempty"`
	Window           string     `json:"window,omitempty"`
	ResetsAt         *time.Time `json:"resets_at,omitempty"`
	Balance          *float64   `json:"balance,omitempty"`
	Threshold        *float64   `json:"threshold,omitempty"`
	Unit             string     `json:"unit,omitempty"`
	ObservedAt       *time.Time `json:"observed_at,omitempty"`
}
type AccountOpsDelivery struct {
	Provider string `json:"provider"`
	// Emit an empty name so receipt retries can clear a previous custom label.
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Attempts   int        `json:"attempts"`
	LastSentAt *time.Time `json:"last_sent_at,omitempty"`
}
type storedOpsWebhook struct {
	ID              string  `json:"id"`
	Name            *string `json:"name,omitempty"`
	Provider        string  `json:"provider"`
	Enabled         bool    `json:"enabled"`
	URLCipher       string  `json:"url_cipher"`
	SecretCipher    string  `json:"secret_cipher,omitempty"`
	Revision        string  `json:"revision"`
	MessageTemplate *string `json:"message_template,omitempty"`
}
type storedOpsConfig struct {
	AccountOpsConfig
	StoredWebhooks []storedOpsWebhook `json:"encrypted_webhooks,omitempty"`
}

var opsID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var opsGroupID = regexp.MustCompile(`^(?:auto:[A-Za-z0-9_-]{16,64}|manual-[A-Za-z0-9_-]{8,64}|account:[0-9]+)$`)
var opsWindowID = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,128}$`)

func (c AccountOpsConfig) hasDestination() bool {
	if c.Recipient != "" {
		return true
	}
	for _, w := range c.Webhooks {
		if w.Enabled && (w.URL != "" || w.URLConfigured || w.urlCipher != "") {
			return true
		}
	}
	return false
}
func (c AccountOpsConfig) hasThreshold() bool {
	for _, r := range c.BalanceThresholds {
		if r.Enabled {
			return true
		}
	}
	return false
}
func (c AccountOpsConfig) hasQuotaThreshold() bool {
	for _, r := range c.QuotaThresholds {
		if r.Enabled {
			return true
		}
	}
	return false
}
func (c AccountOpsConfig) public() AccountOpsConfig {
	c.Webhooks = append([]AccountOpsWebhook{}, c.Webhooks...)
	c.Groups = append([]AccountOpsGroup{}, c.Groups...)
	for i := range c.Groups {
		c.Groups[i].AccountIDs = append([]int64{}, c.Groups[i].AccountIDs...)
	}
	c.UngroupedAccountIDs = append([]int64{}, c.UngroupedAccountIDs...)
	c.BalanceThresholds = append([]AccountOpsBalanceRule{}, c.BalanceThresholds...)
	c.QuotaThresholds = append([]AccountOpsQuotaRule{}, c.QuotaThresholds...)
	normalizeOpsFlags(&c, AccountOpsConfig{})
	for i := range c.Webhooks {
		w := &c.Webhooks[i]
		w.URLConfigured = w.urlCipher != ""
		w.SecretConfigured = w.secretCipher != ""
		w.URL = ""
		w.Secret = ""
		w.ClearSecret = false
		w.urlCipher = ""
		w.secretCipher = ""
		w.revision = ""
	}
	return c
}
func validateAccountOpsURL(provider, raw string) error {
	if provider == "custom" {
		return validateAccountOpsCustomURL(raw)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") || u.Port() != "" || u.RawPath != "" {
		return errors.New("invalid official robot HTTPS URL")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return errors.New("invalid official robot HTTPS URL")
	}
	valid := false
	switch provider {
	case "wecom":
		valid = u.Host == "qyapi.weixin.qq.com" && u.Path == "/cgi-bin/webhook/send" && len(q) == 1 && len(q["key"]) == 1 && q.Get("key") != ""
	case "dingtalk":
		valid = u.Host == "oapi.dingtalk.com" && u.Path == "/robot/send" && len(q) == 1 && len(q["access_token"]) == 1 && q.Get("access_token") != ""
	case "feishu":
		valid = u.Host == "open.feishu.cn" && strings.HasPrefix(u.Path, "/open-apis/bot/v2/hook/") && opsID.MatchString(strings.TrimPrefix(u.Path, "/open-apis/bot/v2/hook/")) && len(q) == 0
	}
	if !valid {
		return errors.New("invalid official robot HTTPS URL")
	}
	return nil
}
func validateAccountOpsExtras(c AccountOpsConfig) error {
	if err := validateAccountOpsChannelName(c.EmailName); err != nil {
		return err
	}
	if len(c.Webhooks) > 5 || len(c.Groups) > 200 || len(c.BalanceThresholds)+len(c.QuotaThresholds) > 1000 {
		return errors.New("too many robot destinations or balance rules")
	}
	if err := validateAccountOpsGroups(c.Groups); err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, w := range c.Webhooks {
		if w.Name != nil {
			if err := validateAccountOpsChannelName(*w.Name); err != nil {
				return err
			}
		}
		if !opsID.MatchString(w.ID) || ids[w.ID] {
			return errors.New("robot IDs must be unique and valid")
		}
		ids[w.ID] = true
		if w.Provider != "wecom" && w.Provider != "dingtalk" && w.Provider != "feishu" && w.Provider != "custom" {
			return errors.New("unsupported robot provider")
		}
		if len(w.URL) > 2048 || len(w.Secret) > 512 {
			return errors.New("robot credential is too long")
		}
		if w.URL != "" {
			if err := validateAccountOpsURL(w.Provider, w.URL); err != nil {
				return err
			}
		}
		if (w.Provider == "wecom" || w.Provider == "custom") && (w.Secret != "" || w.SecretConfigured || w.secretCipher != "") {
			return errors.New("this webhook provider does not support signing secrets")
		}
		if w.Provider == "custom" {
			if _, err := parseAccountOpsTemplate(opsTemplateValue(w.MessageTemplate)); err != nil {
				return err
			}
		} else if opsTemplateValue(w.MessageTemplate) != "" {
			return errors.New("message templates require a custom webhook")
		}
	}
	accounts := map[int64]bool{}
	for _, r := range c.BalanceThresholds {
		if r.AccountID <= 0 || accounts[r.AccountID] || math.IsNaN(r.Threshold) || math.IsInf(r.Threshold, 0) || r.Threshold < 0 || r.Threshold > 1e12 || len(r.Unit) < 1 || len(r.Unit) > 32 || strings.ContainsAny(r.Unit, "\r\n") {
			return errors.New("invalid balance threshold rule")
		}
		accounts[r.AccountID] = true
	}
	for _, r := range c.QuotaThresholds {
		if r.AccountID <= 0 || accounts[r.AccountID] || math.IsNaN(r.ThresholdPercent) || math.IsInf(r.ThresholdPercent, 0) || r.ThresholdPercent <= 0 || r.ThresholdPercent > 100 || (r.Window != "" && !opsWindowID.MatchString(r.Window)) {
			return errors.New("invalid quota threshold rule")
		}
		accounts[r.AccountID] = true
	}
	return nil
}

func validateAccountOpsGroups(groups []AccountOpsGroup) error {
	seenGroups := map[string]bool{}
	seenAccounts := map[int64]bool{}
	for _, group := range groups {
		if !opsGroupID.MatchString(group.ID) || seenGroups[group.ID] {
			return errors.New("invalid or duplicate account group ID")
		}
		seenGroups[group.ID] = true
		name := strings.TrimSpace(group.Name)
		if name != "" {
			if err := validateAccountOpsChannelName(name); err != nil {
				return errors.New("invalid account group name")
			}
		}
		if len(group.AccountIDs) > 1000 {
			return errors.New("too many accounts in an account group")
		}
		for _, id := range group.AccountIDs {
			if id <= 0 || seenAccounts[id] {
				return errors.New("account group members must be positive and unique")
			}
			seenAccounts[id] = true
		}
	}
	return nil
}

func validateAccountOpsChannelName(name string) error {
	if !utf8.ValidString(name) || utf8.RuneCountInString(strings.TrimSpace(name)) > 80 || strings.ContainsFunc(name, unicode.IsControl) {
		return errors.New("channel names must be at most 80 characters and contain no control characters")
	}
	return nil
}

func (s *AccountOpsService) SetNotificationDependencies(accounts AccountRepository, enc SecretEncryptor, fixed bool, timezone string) {
	s.accounts = accounts
	s.encryptor = enc
	s.fixedKey = fixed
	if loc, err := time.LoadLocation(timezone); err == nil {
		s.timezone = loc
	}
}
func (s *AccountOpsService) SetNewAPIGroupReader(reader interface {
	GetBinding(context.Context, int64) (*NewAPIAccountBinding, error)
}) {
	s.newAPIGroupReader = reader
}
func (s *AccountOpsService) EncryptionKeyConfigured() bool { return s.fixedKey && s.encryptor != nil }
func (s *AccountOpsService) loadConfig(ctx context.Context) (AccountOpsConfig, error) {
	c := defaultAccountOpsConfig()
	var raw string
	var err error
	if reader, ok := s.repo.(interface {
		ReadAccountOpsConfig(context.Context) (string, error)
	}); ok {
		raw, err = reader.ReadAccountOpsConfig(ctx)
	} else {
		raw, err = s.settings.GetValue(ctx, accountOpsSettingsKey)
	}
	if errors.Is(err, ErrSettingNotFound) {
		err = nil
	}
	if err != nil {
		return c, err
	}
	st := storedOpsConfig{AccountOpsConfig: c}
	if raw != "" {
		if err = json.Unmarshal([]byte(raw), &st); err != nil {
			return c, errors.New("invalid notification configuration")
		}
	}
	c = st.AccountOpsConfig
	normalizeOpsFlags(&c, AccountOpsConfig{})
	// Plaintext credential fields are never accepted from persisted settings.
	for _, w := range c.Webhooks {
		if w.URL != "" || w.Secret != "" {
			return defaultAccountOpsConfig(), errors.New("invalid stored robot credentials")
		}
	}
	c.Webhooks = nil
	for _, w := range st.StoredWebhooks {
		c.Webhooks = append(c.Webhooks, AccountOpsWebhook{ID: w.ID, Name: w.Name, Provider: w.Provider, Enabled: w.Enabled, URLConfigured: w.URLCipher != "", SecretConfigured: w.SecretCipher != "", MessageTemplate: w.MessageTemplate, urlCipher: w.URLCipher, secretCipher: w.SecretCipher, revision: w.Revision})
	}
	if err = ValidateAccountOpsConfig(c); err != nil {
		return defaultAccountOpsConfig(), errors.New("invalid stored notification configuration")
	}
	return c, nil
}
func (s *AccountOpsService) saveConfig(ctx context.Context, c AccountOpsConfig) error {
	return s.saveConfigWithRuleScope(ctx, c, true)
}
func (s *AccountOpsService) saveConfigWithRuleScope(ctx context.Context, c AccountOpsConfig, all bool) error {
	c.Webhooks = append([]AccountOpsWebhook(nil), c.Webhooks...)
	c.Groups = append([]AccountOpsGroup(nil), c.Groups...)
	for i := range c.Groups {
		c.Groups[i].AccountIDs = append([]int64(nil), c.Groups[i].AccountIDs...)
	}
	c.BalanceThresholds = append([]AccountOpsBalanceRule(nil), c.BalanceThresholds...)
	c.QuotaThresholds = append([]AccountOpsQuotaRule(nil), c.QuotaThresholds...)
	c.Recipient = strings.TrimSpace(c.Recipient)
	if len(c.Webhooks) > 5 || len(c.BalanceThresholds)+len(c.QuotaThresholds) > 1000 {
		return accountOpsConfigValidation("too many robot destinations or threshold rules")
	}
	old, err := s.loadConfig(ctx)
	if err != nil {
		return err
	}
	normalizeOpsFlags(&c, old)
	byID := map[string]AccountOpsWebhook{}
	for _, w := range old.Webhooks {
		byID[w.ID] = w
	}
	for i := range c.Webhooks {
		w := &c.Webhooks[i]
		w.URL = strings.TrimSpace(w.URL)
		prev, ok := byID[w.ID]
		if w.Name == nil {
			w.Name = prev.Name
		}
		if w.Provider == "" || w.Provider == "auto" {
			if w.URL != "" {
				w.Provider, err = detectAccountOpsProvider(w.URL)
				if err != nil {
					return accountOpsConfigValidation(err.Error())
				}
			} else if ok {
				w.Provider = prev.Provider
			} else {
				return accountOpsConfigValidation("webhook URL is required for a new destination")
			}
		}
		if w.MessageTemplate == nil && prev.Provider == w.Provider {
			w.MessageTemplate = prev.MessageTemplate
		}
	}
	if inputErr := validateAccountOpsExtras(c); inputErr != nil {
		return accountOpsConfigValidation(inputErr.Error())
	}
	c.EmailName = strings.TrimSpace(c.EmailName)
	for i := range c.Webhooks {
		w := &c.Webhooks[i]
		if w.Name != nil {
			name := strings.TrimSpace(*w.Name)
			w.Name = &name
		}
		w.URL = strings.TrimSpace(w.URL)
		prev, ok := byID[w.ID]
		w.urlCipher = ""
		w.secretCipher = ""
		w.revision = ""
		w.URLConfigured = false
		w.SecretConfigured = false
		if ok && prev.Provider == w.Provider {
			w.urlCipher = prev.urlCipher
			w.secretCipher = prev.secretCipher
			w.revision = prev.revision
		}
		changed := !ok || prev.Provider != w.Provider || opsTemplateValue(prev.MessageTemplate) != opsTemplateValue(w.MessageTemplate)
		if w.URL != "" || w.Secret != "" || w.ClearSecret {
			if !s.EncryptionKeyConfigured() {
				return errors.New("a fixed encryption key is required for robot credentials")
			}
		}
		if w.URL != "" {
			if err = validateAccountOpsURL(w.Provider, w.URL); err != nil {
				return accountOpsConfigValidation(err.Error())
			}
			if w.secretCipher != "" && w.Secret == "" && !w.ClearSecret {
				previousURL, decryptErr := s.encryptor.Decrypt(prev.urlCipher)
				if decryptErr != nil {
					return errors.New("robot credential decryption failed")
				}
				if previousURL != w.URL {
					w.secretCipher = ""
				}
			}
			w.urlCipher, err = s.encryptor.Encrypt(w.URL)
			if err != nil {
				return errors.New("robot credential encryption failed")
			}
			changed = true
		}
		if w.ClearSecret {
			w.secretCipher = ""
			changed = true
		}
		if w.Secret != "" {
			w.secretCipher, err = s.encryptor.Encrypt(w.Secret)
			if err != nil {
				return errors.New("robot credential encryption failed")
			}
			changed = true
		}
		if w.urlCipher == "" {
			return accountOpsConfigValidation("robot URL is required for a new destination or provider")
		}
		if changed || w.revision == "" {
			w.revision = uuid.NewString()
		}
		w.URLConfigured = true
		w.SecretConfigured = w.secretCipher != ""
	}
	if err = ValidateAccountOpsConfig(c); err != nil {
		return err
	}
	balanceChanged := []AccountOpsBalanceRule{}
	for _, r := range c.BalanceThresholds {
		unchanged := false
		for _, prev := range old.BalanceThresholds {
			if reflect.DeepEqual(r, prev) {
				unchanged = true
				break
			}
		}
		if all || !unchanged {
			balanceChanged = append(balanceChanged, r)
		}
	}
	if err = s.validateBalanceRules(ctx, balanceChanged); err != nil {
		return err
	}
	quotaChanged := []AccountOpsQuotaRule{}
	for _, r := range c.QuotaThresholds {
		unchanged := false
		for _, prev := range old.QuotaThresholds {
			if reflect.DeepEqual(r, prev) {
				unchanged = true
				break
			}
		}
		if all || !unchanged {
			quotaChanged = append(quotaChanged, r)
		}
	}
	if err = s.validateQuotaRules(ctx, quotaChanged); err != nil {
		return err
	}
	for i := range c.QuotaThresholds {
		if c.QuotaThresholds[i].Window == "" {
			c.QuotaThresholds[i].Window = "any"
		}
	}
	st := storedOpsConfig{AccountOpsConfig: c}
	st.Webhooks = []AccountOpsWebhook{}
	for _, w := range c.Webhooks {
		st.StoredWebhooks = append(st.StoredWebhooks, storedOpsWebhook{ID: w.ID, Name: w.Name, Provider: w.Provider, Enabled: w.Enabled, URLCipher: w.urlCipher, SecretCipher: w.secretCipher, Revision: w.revision, MessageTemplate: w.MessageTemplate})
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if writer, ok := s.repo.(interface {
		PersistAccountOpsConfig(context.Context, string, AccountOpsConfig) error
	}); ok {
		if err = writer.PersistAccountOpsConfig(ctx, string(raw), c); err != nil {
			return err
		}
	} else {
		if err = s.settings.Set(ctx, accountOpsSettingsKey, string(raw)); err != nil {
			return err
		}
		if err = s.repo.SuppressDisabled(ctx, c); err != nil {
			return err
		}
	}
	for i := range c.Webhooks {
		c.Webhooks[i].URL = ""
		c.Webhooks[i].Secret = ""
		c.Webhooks[i].ClearSecret = false
	}
	if result, ok := ctx.Value(opsConfigResultKey{}).(*opsConfigResult); ok {
		result.cfg = c
		result.set = true
	} else {
		s.config.Store(c)
	}
	return nil
}
