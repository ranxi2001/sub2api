package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type AccountGroup struct {
	AccountID int64
	GroupID   int64
	Priority  int
	// AllowedModels 限定账号在这个分组里能服务的模型（支持末尾 * 通配）。
	// 为空表示不限制，沿用账号自身支持的模型；它只能在账号支持的范围内收窄，
	// 账号本身是否支持某个模型仍由 IsModelSupported 判断。
	AllowedModels []string
	CreatedAt     time.Time

	Account *Account
	Group   *Group
}

// maxGroupAllowedModels 限制单个分组绑定上的模型条数，防止异常请求写入超大清单。
const maxGroupAllowedModels = 500

// maxGroupAllowedModelLength 与模型名在其他配置里的长度上限保持同一量级。
const maxGroupAllowedModelLength = 200

// NormalizeGroupAllowedModels 去掉首尾空白、空项和重复项，保留管理员填写的顺序。
// 清单为空时返回 nil，表示不限制。
func NormalizeGroupAllowedModels(models []string) []string {
	if len(models) == 0 {
		return nil
	}
	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ValidateGroupAllowedModels 校验按分组设置的模型清单：分组 ID 必须为正数，条数和模型名长度不能超限。
func ValidateGroupAllowedModels(allowed map[int64][]string) error {
	for groupID, models := range allowed {
		if groupID <= 0 {
			return infraerrors.BadRequest("ACCOUNT_GROUP_ALLOWED_MODELS_INVALID_GROUP", "group id must be positive")
		}
		normalized := NormalizeGroupAllowedModels(models)
		if len(normalized) > maxGroupAllowedModels {
			return infraerrors.BadRequest(
				"ACCOUNT_GROUP_ALLOWED_MODELS_TOO_MANY",
				fmt.Sprintf("at most %d models can be allowed per group", maxGroupAllowedModels),
			)
		}
		for _, model := range normalized {
			if len(model) > maxGroupAllowedModelLength {
				return infraerrors.BadRequest(
					"ACCOUNT_GROUP_ALLOWED_MODELS_NAME_TOO_LONG",
					fmt.Sprintf("model names must be at most %d characters", maxGroupAllowedModelLength),
				)
			}
		}
	}
	return nil
}

// GroupAllowedModels 返回账号在指定分组内的模型限制；未绑定该分组或未设置限制时返回 nil。
func (a *Account) GroupAllowedModels(groupID int64) []string {
	if a == nil || groupID <= 0 {
		return nil
	}
	for i := range a.AccountGroups {
		if a.AccountGroups[i].GroupID == groupID {
			return a.AccountGroups[i].AllowedModels
		}
	}
	return nil
}

// IsModelAllowedInGroup 判断账号能否在指定分组里服务该模型。
// 没有分组上下文、没有指定模型、账号在该分组没有设置限制时一律放行。
func (a *Account) IsModelAllowedInGroup(groupID *int64, requestedModel string) bool {
	if a == nil || groupID == nil {
		return true
	}
	if a.gatewayBindingModel != "" {
		requestedModel = a.gatewayBindingModel
	}
	allowed := a.GroupAllowedModels(*groupID)
	if len(allowed) == 0 {
		return true
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return true
	}
	candidates := []string{requestedModel}
	if normalized := normalizeRequestedModelForLookup(a.Platform, requestedModel); normalized != requestedModel {
		candidates = append(candidates, normalized)
	}
	// Anthropic 客户端常发短别名（如 claude-sonnet-4-5），清单里通常写的是完整 ID。
	if a.Platform == PlatformAnthropic {
		if normalized := claude.NormalizeModelID(requestedModel); normalized != requestedModel {
			candidates = append(candidates, normalized)
		}
	}
	for _, pattern := range allowed {
		for _, candidate := range candidates {
			if matchWildcard(pattern, candidate) {
				return true
			}
		}
	}
	return false
}

// groupAllowedConcreteModels 返回账号在分组限制清单里的具体模型名（去掉通配项），
// 供模型列表公布没有映射、但在分组里被限制了模型的账号。
func groupAllowedConcreteModels(account *Account, groupID *int64) []string {
	if account == nil || groupID == nil {
		return nil
	}
	allowed := account.GroupAllowedModels(*groupID)
	out := make([]string, 0, len(allowed))
	for _, model := range allowed {
		if strings.Contains(model, "*") {
			continue
		}
		out = append(out, model)
	}
	return out
}

// accountsAllowedInGroupForModel 返回在本分组里允许服务该模型的账号。
// 没有账号因分组限制被排除时原样返回入参，不产生拷贝。
func accountsAllowedInGroupForModel(accounts []Account, groupID *int64, modelID string) []Account {
	if groupID == nil {
		return accounts
	}
	for i := range accounts {
		if accounts[i].IsModelAllowedInGroup(groupID, modelID) {
			continue
		}
		filtered := make([]Account, 0, len(accounts)-1)
		filtered = append(filtered, accounts[:i]...)
		for j := i + 1; j < len(accounts); j++ {
			if accounts[j].IsModelAllowedInGroup(groupID, modelID) {
				filtered = append(filtered, accounts[j])
			}
		}
		return filtered
	}
	return accounts
}

// IsModelSupportedInGroup 同时检查账号自身是否支持该模型、以及分组是否允许它服务该模型。
// 供没有平台特殊映射逻辑的调度路径使用；需要平台映射的路径分别调用两者。
func (a *Account) IsModelSupportedInGroup(groupID *int64, requestedModel string) bool {
	return a.IsModelSupported(requestedModel) && a.IsModelAllowedInGroup(groupID, requestedModel)
}
