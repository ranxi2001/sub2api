package service

import "fmt"

const (
	qwenSessionCacheCredential  = "qwen_session_cache_enabled"
	qwenStrictSessionCredential = "qwen_strict_session_affinity"
)

// These switches are opt-in; existing Qwen Chat Completions accounts retain
// their protocol and normal affinity behavior until an administrator enables them.
func (a *Account) QwenSessionCacheEnabled() bool {
	if a == nil || a.Platform != PlatformQwen || a.Type != AccountTypeAPIKey {
		return false
	}
	enabled, _ := a.Credentials[qwenSessionCacheCredential].(bool)
	return enabled
}

func (a *Account) QwenStrictSessionAffinity() bool {
	if a == nil || a.Platform != PlatformQwen || a.Type != AccountTypeAPIKey {
		return false
	}
	enabled, _ := a.Credentials[qwenStrictSessionCredential].(bool)
	return enabled
}

func validateQwenCacheCredentials(credentials map[string]any) error {
	for _, key := range []string{qwenSessionCacheCredential, qwenStrictSessionCredential} {
		if value, exists := credentials[key]; exists {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s must be a boolean", key)
			}
		}
	}
	return nil
}
