package handler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// noAccountErrorClassification describes the HTTP response to emit when
// account selection failed with ErrNoAvailableAccounts. Handlers obtain it
// via classifyNoAccountError and choose between:
//
//   - 404 model_not_found — the group has accounts, but none of them are
//     configured to serve the requested model (config / typo / unsupported
//     model). Returning 503 here misleads operators and trips reverse-proxy
//     health checks; 404 lets the client surface the real problem.
//
//   - 503 api_error — accounts that could serve the model exist but are
//     temporarily exhausted (rate limit, quota auto-pause, runtime block) OR
//     the group has no accounts at all. Both stay on 503 because retrying
//     after a backoff can plausibly succeed (or, in the empty-pool case, the
//     operator may be in the middle of adding accounts).
type noAccountErrorClassification struct {
	Status        int
	ErrType       string
	Message       string
	ModelNotFound bool // true when this is a 404 model_not_found classification
	Code          string
	RetryAt       *time.Time
	Diagnosis     service.ModelAvailabilityDiagnosis
}

var selectionImagePoolPattern = regexp.MustCompile(`\bpool=(\d+), filtered:.*\bimage_model_(not_supported|not_authorized|rate_limited)=(\d+)(?:\s|\)|$)`)

var selectionModelRateLimitedPattern = regexp.MustCompile(`(?:model_rate_limited|rate_limited)=(\d+)`)

// classifySelectionFailureError preserves the scheduler's compact reason when
// every model-capable account is temporarily rate limited.
func classifySelectionFailureError(err error, fallback noAccountErrorClassification) noAccountErrorClassification {
	if err == nil {
		return fallback
	}
	if errors.Is(err, service.ErrPriorityLatencyUnavailable) {
		return noAccountErrorClassification{Status: http.StatusServiceUnavailable, ErrType: "temporarily_unavailable", Code: "latency_penalty", Message: "Latency scheduling is temporarily unavailable; retry after 5 seconds"}
	}
	if errors.Is(err, service.ErrOpenAIRPMExhausted) {
		return noAccountErrorClassification{
			Status:  http.StatusTooManyRequests,
			ErrType: "rate_limit_error",
			Message: "All eligible OpenAI OAuth accounts are at their per-minute request limit. Please retry after the current minute resets.",
		}
	}
	if errors.Is(err, service.ErrOpenAIRPMUnavailable) {
		return noAccountErrorClassification{
			Status:  http.StatusServiceUnavailable,
			ErrType: "api_error",
			Message: "OpenAI OAuth RPM protection is temporarily unavailable. Please retry later.",
		}
	}
	// A 404 model_not_found fallback is authoritative and must not be downgraded
	// to a rate-limit verdict. classifyNoAccountError only reaches it through
	// DiagnoseModelAvailabilityForPlatform, a dedicated database query over
	// persistent eligibility (active + schedulable + model_mapping) that already
	// established no account in the group can serve this model at all. A transient
	// per-model cooldown on one of the remaining candidates does not make "all
	// available accounts are rate-limited" true.
	//
	// Reporting 429 here is actively harmful: retrying can never succeed, and
	// clients that treat 429 as a rate limit retry hard and swallow the body
	// (Codex surfaces only "exceeded retry limit, last status: 429"), losing the
	// one message that names the real problem. It also flips the ops attribution
	// from a local model-configuration issue to routing capacity, because call
	// sites gate markOpsRoutingCapacityLimitedIfNoAvailable on ModelNotFound.
	if fallback.ModelNotFound || fallback.Code != "" {
		return fallback
	}
	// Native image tools require a second capability beyond the outer text
	// model. A text-only availability query must not hide that exclusion, or
	// mark the text model as absent. Only classify a unanimous pool verdict.
	if match := selectionImagePoolPattern.FindStringSubmatch(err.Error()); len(match) == 4 {
		pool, poolErr := strconv.Atoi(match[1])
		blocked, blockedErr := strconv.Atoi(match[3])
		if poolErr == nil && blockedErr == nil && pool > 0 && pool == blocked {
			fallback.Code = "image_capability_unavailable"
			fallback.Message = "No eligible account can serve the requested image-generation tool. For a text-only turn, remove the image tool declaration; otherwise use a group with image-capable accounts."
			if match[2] == "rate_limited" {
				fallback.Code = "image_capacity_exhausted"
				fallback.Message = "All eligible accounts are temporarily rate-limited for the requested image-generation tool. Please retry after image capacity recovers."
			}
			return fallback
		}
	}
	match := selectionModelRateLimitedPattern.FindStringSubmatch(strings.ToLower(err.Error()))
	if len(match) != 2 {
		return fallback
	}
	count, parseErr := strconv.Atoi(match[1])
	if parseErr != nil || count <= 0 {
		return fallback
	}
	return noAccountErrorClassification{
		Status:  http.StatusTooManyRequests,
		ErrType: "rate_limit_error",
		Message: "All available accounts are currently rate-limited. Please retry later.",
	}
}

// classifyNoAccountError decides between 404 model_not_found and 503
// api_error for "no available accounts" failures.
//
// The classifier intentionally does not consume the original error: the
// selection layer never tells us *why* the pool came up empty (rate-limited
// vs. unsupported model are both wrapped as ErrNoAvailableAccounts). Instead
// we re-check pool composition through DiagnoseModelAvailabilityForPlatform.
// Its dedicated database query considers only persistent eligibility
// (active status + schedulable setting) and model_mapping, bypassing scheduler
// snapshots and transient filters. That guarantees a 404 is only returned
// when no configured candidate supports the model. Expiring upstream model
// rejection observations still block scheduling but never remove configured
// support: returning 404 for them would renew negative caches on another relay.
//
// routingModel is the model name that account selection actually compared
// against (i.e. after group-level dispatch mapping). displayModel is the
// raw model the caller asked for; it is used only in the user-facing error
// message so that internal mapping details don't leak. Most callers pass
// the same value for both.
//
// platform is the platform the request was routed to (use
// service.PlatformOpenAI / PlatformAnthropic / PlatformGemini). It is
// required because Anthropic/Gemini routes additionally surface
// mixed-scheduled Antigravity accounts; passing the wrong platform would
// flip a legitimate 503 to a misleading 404 (or vice versa).
func classifyNoAccountError(
	ctx context.Context,
	diag service.ModelAvailabilityDiagnoser,
	apiKey *service.APIKey,
	routingModel string,
	displayModel string,
	platform string,
) noAccountErrorClassification {
	fallback := noAccountErrorClassification{
		Status:  http.StatusServiceUnavailable,
		ErrType: "api_error",
		Message: "Service temporarily unavailable",
	}

	routingModel = strings.TrimSpace(routingModel)
	displayModel = strings.TrimSpace(displayModel)
	if displayModel == "" {
		displayModel = routingModel
	}
	if diag == nil || apiKey == nil || apiKey.GroupID == nil || routingModel == "" {
		return fallback
	}

	result := diag.DiagnoseModelAvailabilityForPlatform(ctx, apiKey.GroupID, routingModel, platform)
	fallback.Diagnosis = result
	if result.AllModelAccountsRateLimited() {
		fallback.Code = "model_capacity_exhausted"
		fallback.RetryAt = result.EarliestRateLimitResetAt
		fallback.Message = fmt.Sprintf("Accounts supporting model %q are currently rate-limited. Earliest quota reset is %s; availability may recover later.", displayModel, fallback.RetryAt.UTC().Format(time.RFC3339))
		return fallback
	}
	if result.HasAccountsInPool && !result.HasModelSupport {
		message := fmt.Sprintf("Model %q is not supported by any configured account in this group", displayModel)
		if result.HasRetiredSubscriptionModel {
			message = fmt.Sprintf("Model %q targets GPT-5.5, which retired from Codex with ChatGPT sign-in on October 14, 2026. Select an available replacement such as GPT-6.1 Sol or GPT-6 Luna, or configure an API-key upstream that supports GPT-5.5.", displayModel)
		}
		return noAccountErrorClassification{
			Status:        http.StatusNotFound,
			ErrType:       "model_not_found",
			Message:       message,
			ModelNotFound: true,
			Code:          "model_not_found",
			Diagnosis:     result,
		}
	}
	return fallback
}

// classifyNoAccountErrorFromGin is a thin wrapper that forwards the gin
// context's underlying request context. Most call sites already have a
// *gin.Context handy, so this keeps the call sites uncluttered.
func classifyNoAccountErrorFromGin(
	c *gin.Context,
	diag service.ModelAvailabilityDiagnoser,
	apiKey *service.APIKey,
	routingModel string,
	displayModel string,
	platform string,
) noAccountErrorClassification {
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	classification := classifyNoAccountError(ctx, diag, apiKey, routingModel, displayModel, platform)
	if classification.Diagnosis.Complete && c != nil {
		d := classification.Diagnosis
		requestLogger(c, "handler.model_availability").Warn("gateway.model_capacity_diagnosis",
			zap.String("requested_model", displayModel), zap.String("routing_model", routingModel),
			zap.String("platform", platform), zap.Int64("group_id", *apiKey.GroupID),
			zap.Int("configured_accounts", d.ConfiguredAccounts),
			zap.Int("model_supported_accounts", d.ModelSupportedAccounts),
			zap.Int("model_not_supported_accounts", d.ConfiguredAccounts-d.ModelSupportedAccounts),
			zap.Int("model_rate_limited_accounts", d.ModelRateLimitedAccounts),
			zap.Any("earliest_rate_limit_reset_at", d.EarliestRateLimitResetAt),
		)
	}
	if classification.RetryAt != nil && c != nil && !c.Writer.Written() {
		seconds := int64(math.Ceil(time.Until(*classification.RetryAt).Seconds()))
		if seconds < 1 {
			seconds = 1
		}
		c.Header("Retry-After", strconv.FormatInt(seconds, 10))
	}
	if classification.ModelNotFound {
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalModelConfiguration)
	}
	return classification
}

func classifyOpenAICompatibleNoAccountErrorFromGin(
	c *gin.Context,
	diag service.ModelAvailabilityDiagnoser,
	apiKey *service.APIKey,
	routingModel string,
	displayModel string,
) noAccountErrorClassification {
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	return classifyNoAccountErrorFromGin(
		c,
		diag,
		apiKey,
		routingModel,
		displayModel,
		openAICompatibleRequestPlatform(ctx, apiKey),
	)
}

func openAICompatibleSelectionErrorForLog(err error, platform string) error {
	if err == nil || platform != service.PlatformGrok {
		return err
	}
	message := strings.ReplaceAll(err.Error(), "OpenAI accounts", "Grok accounts")
	if message == err.Error() {
		return err
	}
	return fmt.Errorf("%s", message)
}
