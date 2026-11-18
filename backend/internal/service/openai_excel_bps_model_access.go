package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var excelBPSModelID = regexp.MustCompile(`^gpt-[a-z0-9]+(?:[._-][a-z0-9]+)*$`)

// Convert only the official access response. Empty/denied access is authoritative;
// malformed envelopes and HTTP failures cannot manufacture a local allowlist.
func excelBPSAccessModelsBody(raw []byte) ([]byte, error) {
	var envelope struct {
		Allowed *bool `json:"allowed"`
		Catalog *struct {
			Models     *[]json.RawMessage `json:"models"`
			Restricted []string           `json:"restricted_models"`
		} `json:"model_catalog"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Allowed == nil {
		return nil, fmt.Errorf("invalid Excel BPS access response")
	}
	models := make([]map[string]any, 0)
	if *envelope.Allowed {
		if envelope.Catalog == nil || envelope.Catalog.Models == nil {
			return nil, fmt.Errorf("missing Excel BPS model catalog")
		}
		restricted := map[string]bool{}
		for _, id := range envelope.Catalog.Restricted {
			restricted[id] = true
		}
		seen := map[string]bool{}
		for _, raw := range *envelope.Catalog.Models {
			var entry struct {
				ID        string `json:"id"`
				Label     string `json:"label"`
				Available *bool  `json:"available"`
				Enabled   *bool  `json:"enabled"`
				Picker    *bool  `json:"model_picker_enabled"`
				Policy    *struct {
					State string `json:"state"`
				} `json:"policy"`
				Efforts []struct {
					Value string `json:"value"`
				} `json:"efforts"`
			}
			if json.Unmarshal(raw, &entry) != nil || !excelBPSModelID.MatchString(entry.ID) || restricted[entry.ID] || seen[entry.ID] {
				continue
			}
			if entry.Available != nil && !*entry.Available || entry.Enabled != nil && !*entry.Enabled || entry.Picker != nil && !*entry.Picker || entry.Policy != nil && (entry.Policy.State == "disabled" || entry.Policy.State == "unconfigured") {
				continue
			}
			model := map[string]any{"id": entry.ID, "object": "model", "owned_by": "openai", "created": 0}
			if label := strings.TrimSpace(entry.Label); label != "" {
				model["display_name"] = label
			}
			efforts := make([]string, 0)
			effortSeen := map[string]bool{}
			for _, effort := range entry.Efforts {
				switch effort.Value {
				case "low", "medium", "high", "xhigh":
					if !effortSeen[effort.Value] {
						efforts = append(efforts, effort.Value)
						effortSeen[effort.Value] = true
					}
				}
			}
			if entry.Efforts != nil {
				model["reasoning_efforts"] = efforts
				model["supported_reasoning_levels"] = efforts
			}
			models = append(models, model)
			seen[entry.ID] = true
		}
	}
	return json.Marshal(map[string]any{"object": "list", "data": models})
}

// FetchExcelBPSModelsList uses a separate fresh-only cache scoped by bearer,
// account and proxy identity. Expired permissions are never served stale.
func (s *OpenAIGatewayService) FetchExcelBPSModelsList(ctx context.Context, account *Account) (*OpenAIModelsResponse, error) {
	return s.fetchExcelBPSModelsListWithPolicy(ctx, account, openAIModelsReadDefault)
}

func (s *OpenAIGatewayService) fetchExcelBPSModelsListWithPolicy(ctx context.Context, account *Account, policy openAIModelsReadPolicy) (*OpenAIModelsResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.httpUpstream == nil || account == nil || !account.IsOpenAI() || !account.IsExcelBPSEnabled() {
		return nil, fmt.Errorf("excel BPS model discovery is unavailable")
	}
	var token string
	var err error
	if policy == openAIModelsReadWhitelist {
		token, err = s.getExcelBPSAccessTokenReadOnly(ctx, account)
	} else {
		token, err = s.getExcelBPSAccessToken(ctx, account)
	}
	if err != nil {
		if IsExcelAuthorizationError(err) {
			return nil, err
		}
		return nil, infraerrors.New(502, "EXCEL_BPS_MODELS_AUTH_UNAVAILABLE", "Excel BPS credentials are unavailable")
	}
	accountID := excelBPSAccountID(account, token)
	if accountID == "" {
		return nil, fmt.Errorf("excel BPS model discovery requires an account ID")
	}
	request, err := newExcelBPSRequest(ctx, nil, token, accountID)
	if err != nil {
		return nil, err
	}
	request.Method = http.MethodGet
	request.Body = nil
	request.ContentLength = 0
	request.Header.Set("Accept", "application/json")
	if policy == openAIModelsReadWhitelist {
		request.Header.Set("Cache-Control", "no-cache")
		request.Header.Del("If-None-Match")
	}
	request.URL.Path = "/basispoints/api/responses/access"
	request.URL.RawQuery = "include_models=true"
	proxy := upstreamModelsProxyURL(account)
	identity := openAIModelsRequest{url: request.URL.String(), headers: request.Header, proxyURL: fmt.Sprintf("%s|bps_pool:%s|managed:%t", proxy, account.ExcelBPSProxySource(), account.IsExcelBPSMihomoEnabled()), accountID: account.ID, credentialAccountID: account.ID, standardModelsList: true}
	key := buildOpenAIModelsCacheKey(identity)
	if policy != openAIModelsReadWhitelist {
		if cached, state := s.excelBPSModelsCache.get(key, time.Now()); state == openAIModelsCacheFresh {
			return openAIModelsResponseForClient(cached, ""), nil
		}
	}
	fetch := func(fetchCtx context.Context) (*OpenAIModelsResponse, error) {
		if policy != openAIModelsReadWhitelist {
			if cached, state := s.excelBPSModelsCache.get(key, time.Now()); state == openAIModelsCacheFresh {
				return cached, nil
			}
		}
		fetchCtx, cancel := context.WithTimeout(fetchCtx, 5*time.Second)
		defer cancel()
		var lease excelBPSLease
		if account.IsExcelBPSMihomoEnabled() || account.ExcelBPSRequestProxyEnabled() {
			proxy, lease, err = s.excelBPSAcquireFor(account)(fetchCtx, fmt.Sprintf("transient:bps-models:%d", account.ID))
			if err != nil {
				return nil, fmt.Errorf("excel BPS discovery proxy is unavailable")
			}
			defer lease.Release()
		}
		req := request.Clone(WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(fetchCtx, HTTPUpstreamProfileExcelBPS)))
		if err = s.fenceAuthConnectionEgress(fetchCtx, req, account); err != nil {
			return nil, err
		}
		response, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
		if err != nil {
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			return nil, infraerrors.New(502, "EXCEL_BPS_MODELS_UPSTREAM_FAILED", "Excel BPS model discovery request failed")
		}
		if response == nil || response.Body == nil {
			return nil, fmt.Errorf("excel BPS model discovery returned no response")
		}
		defer func() { _ = response.Body.Close() }()
		stop := context.AfterFunc(fetchCtx, func() { _ = response.Body.Close() })
		defer stop()
		if response.StatusCode != http.StatusOK {
			return nil, infraerrors.New(502, "EXCEL_BPS_MODELS_UPSTREAM_REJECTED", fmt.Sprintf("Excel BPS model discovery returned HTTP %d", response.StatusCode))
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, openAIModelsCacheBodyLimit+1))
		if err != nil || len(raw) > openAIModelsCacheBodyLimit {
			return nil, fmt.Errorf("excel BPS model catalog could not be read")
		}
		body, err := excelBPSAccessModelsBody(raw)
		if err != nil {
			return nil, err
		}
		if err := fetchCtx.Err(); err != nil {
			return nil, err
		}
		manifest := &OpenAIModelsResponse{Body: body, ETag: codexModelsManifestBodyETag(body)}
		if policy != openAIModelsReadWhitelist {
			s.excelBPSModelsCache.set(key, manifest, time.Now())
		}
		return manifest, nil
	}
	if policy == openAIModelsReadWhitelist {
		// No shared cache, detached singleflight or background work can verify
		// the current destructive edit. Cancellation belongs to this caller.
		return fetch(ctx)
	}
	if HasAPIKeyAdmissionOwner(ctx) {
		result, err := fetch(ctx)
		if err != nil {
			return nil, err
		}
		return openAIModelsResponseForClient(result, ""), nil
	}
	ch := s.excelBPSModelsCache.refresh.DoChan(key, func() (any, error) { return fetch(context.Background()) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-ch:
		if result.Err != nil {
			return nil, result.Err
		}
		manifest, ok := result.Val.(*OpenAIModelsResponse)
		if !ok || manifest == nil {
			return nil, fmt.Errorf("excel BPS model discovery returned no catalog")
		}
		return openAIModelsResponseForClient(manifest, ""), nil
	}
}

func mergeExcelBPSAccountModels(native, bps []byte, account *Account) ([]byte, error) {
	_, bpsEntries, err := modelCatalogEntries(bps, "data")
	if err != nil {
		return nil, err
	}
	var nativeEntries []json.RawMessage
	if len(native) > 0 {
		_, nativeEntries, err = modelCatalogEntries(native, "data")
		if err != nil {
			return nil, err
		}
	}
	merged := make([]json.RawMessage, 0, len(nativeEntries)+len(bpsEntries))
	for _, batch := range []struct {
		entries []json.RawMessage
		bps     bool
	}{{nativeEntries, false}, {bpsEntries, true}} {
		for _, raw := range batch.entries {
			var entry struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw, &entry) != nil {
				continue
			}
			if account.isExcelBPSUpstreamModelEnabled(entry.ID) == batch.bps {
				merged = append(merged, raw)
			}
		}
	}
	return json.Marshal(map[string]any{"object": "list", "data": merged})
}

// Apply permissions/efforts to raw upstream IDs before public-name projection.
func applyExcelBPSEffortsToManifest(body, access []byte, account *Account) ([]byte, error) {
	envelope, entries, err := modelCatalogEntries(body, "models")
	if err != nil {
		return nil, err
	}
	_, models, err := modelCatalogEntries(access, "data")
	if err != nil {
		return nil, err
	}
	byID := make(map[string]map[string]json.RawMessage, len(models))
	for _, raw := range models {
		var model map[string]json.RawMessage
		_ = json.Unmarshal(raw, &model)
		var id string
		_ = json.Unmarshal(model["id"], &id)
		byID[id] = model
	}
	kept := make([]json.RawMessage, 0, len(entries))
	for _, raw := range entries {
		var model map[string]json.RawMessage
		_ = json.Unmarshal(raw, &model)
		var slug string
		_ = json.Unmarshal(model["slug"], &slug)
		if account.isExcelBPSUpstreamModelEnabled(slug) {
			capability, allowed := byID[slug]
			if !allowed {
				continue
			}
			if label := capability["display_name"]; label != nil {
				model["display_name"] = label
			}
			if levels := capability["reasoning_efforts"]; levels != nil {
				var efforts []string
				_ = json.Unmarshal(levels, &efforts)
				levels := make([]map[string]string, 0, len(efforts))
				for _, effort := range efforts {
					levels = append(levels, map[string]string{"effort": effort, "description": effort})
				}
				model["supported_reasoning_levels"], _ = json.Marshal(levels)
				if len(efforts) > 0 {
					model["default_reasoning_level"], _ = json.Marshal(efforts[0])
				}
			}
			model["multi_agent_version"] = json.RawMessage("null")
			model["multi_agent_reasoning_effort"] = json.RawMessage("null")
			raw, _ = json.Marshal(model)
		}
		kept = append(kept, raw)
	}
	envelope["models"], _ = json.Marshal(kept)
	return json.Marshal(envelope)
}

// Independent BPS discovery applies to the actual selected model route. Native
// models keep their own source; a native source failure cannot invent BPS access.
func (s *OpenAIGatewayService) fetchExcelBPSAccountModels(ctx context.Context, account *Account) (*OpenAIModelsResponse, error) {
	return s.fetchExcelBPSAccountModelsWithPolicy(ctx, account, false)
}

func (s *OpenAIGatewayService) fetchExcelBPSAccountModelsWithPolicy(ctx context.Context, account *Account, requireComplete bool) (*OpenAIModelsResponse, error) {
	policy := openAIModelsReadDefault
	if requireComplete {
		policy = openAIModelsReadWhitelist
	}
	access, accessErr := s.fetchExcelBPSModelsListWithPolicy(ctx, account, policy)
	if accessErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if requireComplete || account.IsExcelOAuth() || account.isExcelBPSAllModelsEnabled() {
			return nil, accessErr
		}
		access = &OpenAIModelsResponse{Body: []byte(`{"object":"list","data":[]}`)}
	}
	var native []byte
	if !account.IsExcelOAuth() && !account.isExcelBPSAllModelsEnabled() {
		response, err := s.fetchNativeOpenAIModelsListWithPolicy(ctx, account, policy)
		if err == nil {
			native = response.Body
		} else if requireComplete {
			return nil, err
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	body, err := mergeExcelBPSAccountModels(native, access.Body, account)
	if err != nil {
		return nil, err
	}
	if accessErr != nil {
		_, entries, parseErr := modelCatalogEntries(body, "data")
		if parseErr != nil || len(entries) == 0 {
			return nil, accessErr
		}
	}
	return &OpenAIModelsResponse{Body: body, ETag: codexModelsManifestBodyETag(body)}, nil
}

func (s *OpenAIGatewayService) fetchExcelBPSCodexManifest(ctx context.Context, account *Account, ifNoneMatch string) (*OpenAIModelsResponse, error) {
	access, err := s.FetchExcelBPSModelsList(ctx, account)
	if err != nil {
		return nil, err
	}
	bpsBody, err := mergeExcelBPSAccountModels(nil, access.Body, account)
	if err != nil {
		return nil, err
	}
	_, entries, err := modelCatalogEntries(bpsBody, "data")
	if err != nil {
		return nil, err
	}
	var body []byte
	if len(entries) == 0 {
		body = []byte(`{"models":[]}`)
	} else {
		body = convertOpenAIModelListToCodexManifestForAccount(bpsBody, account)
	}
	body, err = applyExcelBPSEffortsToManifest(body, access.Body, account)
	if err != nil {
		return nil, err
	}
	if !account.IsExcelOAuth() && !account.isExcelBPSAllModelsEnabled() {
		native, nativeErr := s.fetchNativeCodexModelsManifest(ctx, account, CodexCanonicalClientVersion(), "")
		if nativeErr == nil {
			envelope, models, err := modelCatalogEntries(native.Body, "models")
			if err != nil {
				return nil, err
			}
			kept := make([]json.RawMessage, 0, len(models))
			for _, raw := range models {
				var entry struct {
					Slug string `json:"slug"`
				}
				_ = json.Unmarshal(raw, &entry)
				if !account.isExcelBPSUpstreamModelEnabled(entry.Slug) {
					kept = append(kept, raw)
				}
			}
			envelope["models"], _ = json.Marshal(kept)
			nativeBody, _ := json.Marshal(envelope)
			body, err = mergeCodexModelsManifestBodies([][]byte{nativeBody, body})
			if err != nil {
				return nil, err
			}
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	manifest := &OpenAIModelsResponse{Body: body, ETag: codexModelsManifestBodyETag(body)}
	return openAIModelsResponseForClient(manifest, ifNoneMatch), nil
}
