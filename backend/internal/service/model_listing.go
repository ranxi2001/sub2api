package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// ModelListingSource 汇总模型列表过滤的候选来源：账号映射键（availableModels）
// 与平台默认列表（fallbackModels）。账号映射为空时回落默认列表；Anthropic
// 平台两者取并集，其余平台以账号映射键为准。
func ModelListingSource(platform string, availableModels, fallbackModels []string) []string {
	if availableModels != nil && len(availableModels) == 0 {
		return availableModels
	}
	if platform == PlatformOpenAI && availableModels != nil {
		return availableModels
	}
	if len(availableModels) == 0 {
		return fallbackModels
	}
	if platform == PlatformAnthropic {
		return mergeModelIDs(availableModels, fallbackModels)
	}
	return availableModels
}

// DefaultModelIDsForPlatform returns the existing gateway model catalog for a platform.
func DefaultModelIDsForPlatform(platform string) []string {
	switch platform {
	case PlatformOpenAI:
		return openai.DefaultModelIDs()
	case PlatformGemini:
		ids := make([]string, 0, len(geminicli.DefaultModels))
		for _, model := range geminicli.DefaultModels {
			ids = append(ids, model.ID)
		}
		return ids
	case PlatformAntigravity:
		models := antigravity.DefaultModels()
		ids := make([]string, 0, len(models))
		for _, model := range models {
			ids = append(ids, model.ID)
		}
		return ids
	case PlatformAnthropic:
		return claude.DefaultModelIDs()
	case PlatformGrok:
		return xai.DefaultModelIDs()
	case PlatformOpenCodeGo:
		return DefaultOpenCodeGoModelIDs()
	case PlatformTypeSafe:
		return []string{"jev-latest"}
	case PlatformComposite:
		ids := make([]string, 0)
		seen := make(map[string]struct{})
		for _, concretePlatform := range domain.CompositePrecedencePlatformIDs() {
			// TypeSafe is deliberately skipped: jev-latest only works through
			// /v1/systemone, so the static fallback never advertises it to LLM
			// clients. compositeAvailableModels lists it when the group can serve it.
			if concretePlatform == PlatformTypeSafe {
				continue
			}
			for _, id := range DefaultModelIDsForPlatform(concretePlatform) {
				if _, ok := seen[id]; ok {
					continue
				}
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
		return ids
	default:
		ids := make([]string, 0, len(claude.DefaultModels))
		for _, model := range claude.DefaultModels {
			ids = append(ids, model.ID)
		}
		return ids
	}
}

func mergeModelIDs(primary, secondary []string) []string {
	seen := make(map[string]struct{}, len(primary)+len(secondary))
	merged := make([]string, 0, len(primary)+len(secondary))
	for _, models := range [][]string{primary, secondary} {
		for _, model := range models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			if _, ok := seen[model]; ok {
				continue
			}
			seen[model] = struct{}{}
			merged = append(merged, model)
		}
	}
	return merged
}
