package admin

import (
	"context"
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Separate from usage-bill synchronization. If settlement evidence is requested,
// only a first-batch 404/405 denotes an unsupported endpoint; a partial or
// invalid lookup cannot prove settlement.
//
//nolint:unused // settlement lookup is intentionally unwired; wallet debits are not a bill source
func readProviderProfitProofs(ctx context.Context, s *providerSession, rows []service.ProviderProfitRemoteCharge) error {
	for start := 0; start < len(rows); start += 100 {
		batch := rows[start:min(start+100, len(rows))]
		ids := make([]int64, 0, len(batch))
		requested := make(map[int64]*service.ProviderProfitRemoteCharge, len(batch))
		for i := range batch {
			ids = append(ids, batch[i].UsageID)
			requested[batch[i].UsageID] = &batch[i]
		}
		data, env, err := s.call(ctx, http.MethodPost, "/api/v1/usage/settled-debits", map[string]any{"usage_ids": ids}, nil, true)
		if err != nil {
			var remote *providerRemoteError
			var api *providerAPIError
			var status int
			if errors.As(err, &remote) {
				status = remote.status
			} else if errors.As(err, &api) {
				status = api.status
			}
			if start == 0 && (status == http.StatusNotFound || status == http.StatusMethodNotAllowed) {
				return nil
			}
			return err
		}
		o, _ := data.(map[string]any)
		items, ok := o["items"].([]any)
		if !ok || !profitSuccess(env) || len(items) > len(batch) {
			return errProviderResponse
		}
		for _, raw := range items {
			proof, _ := raw.(map[string]any)
			id, idOK := profitInt(proof["usage_id"])
			key, keyOK := profitInt(proof["api_key_id"])
			user, userOK := profitInt(proof["user_id"])
			r := requested[id]
			text, stringOK := proof["amount"].(string)
			amount, amountOK := profitDecimal(text)
			if !idOK || !keyOK || !userOK || r == nil || r.KeyID != key || r.UserID != user || r.RequestID != proof["request_id"] || proof["currency"] != "USD" || !stringOK || !amountOK || amount == "0" {
				return errProviderResponse
			}
			r.SettledAmount = &amount
			delete(requested, id)
		}
	}
	return nil
}
