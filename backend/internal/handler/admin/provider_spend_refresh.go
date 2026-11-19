package admin

import (
	"context"
	"log"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// refreshBoundProviderSpend probes each linked provider for the selected
// window, then the report prices from that snapshot. A reading observed within
// ProfitSpendFreshness is reused unless force is set. A failed probe keeps the
// last good amount for the same window. Unbound estimate rows are not blocked.
func (h *ChannelHandler) refreshBoundProviderSpend(parent context.Context, from, until time.Time, force bool) {
	if h == nil || h.channelService == nil || !until.After(from) {
		return
	}
	repo, err := h.channelService.MatureProviderRepository()
	if err != nil {
		return
	}
	store, ok := repo.(service.ProviderSpendStore)
	if !ok || h.providerEncryptor == nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	targets, err := store.ListProviderSpendTargets(ctx)
	if err != nil {
		log.Printf("[ProfitOperations] spend refresh list failed error_type=%T", err)
		return
	}
	for _, target := range targets {
		if ctx.Err() != nil {
			return
		}
		if !force {
			fresh, freshErr := store.ProviderSpendFresh(ctx, target.ID, from, until, service.ProfitSpendFreshness)
			if freshErr == nil && fresh {
				continue
			}
		}
		h.refreshOneProviderSpend(ctx, store, target, from, until)
	}
}

func (h *ChannelHandler) refreshOneProviderSpend(parent context.Context, store service.ProviderSpendStore, target service.ProviderSpendTarget, from, until time.Time) {
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	client, err := h.matureProviderClient()
	if err != nil {
		return
	}
	defer client.CloseIdleConnections()
	var probe providerProbe
	var probeErr error
	probed := false
	repo, err := h.channelService.MatureProviderRepository()
	if err != nil {
		return
	}
	_, sessionErr := repo.WithMatureProviderSession(ctx, target.ID, target.Revision, func(operationCtx context.Context, p *service.MatureProvider, cipher string) (service.ProviderSessionUpdate, error) {
		u := service.ProviderSessionUpdate{}
		if cipher == "" {
			return u, service.ErrMatureProviderValidation
		}
		secret, decryptErr := h.providerEncryptor.Decrypt(cipher)
		if decryptErr != nil {
			return u, service.ErrMatureProviderValidation
		}
		probed = true
		probe, probeErr = probeProviderSpend(operationCtx, providerProtocolRecord(p), secret, client, from, until)
		if probe.secret != "" {
			renewed, encryptErr := h.providerEncryptor.Encrypt(probe.secret)
			if encryptErr != nil {
				return u, encryptErr
			}
			u.Cipher = &renewed
		}
		// Only the consumption-day window may update the channel-operations card.
		if service.ProfitWindowUsesWalletToday(from, until, time.Now()) {
			if probeErr != nil {
				u.Status = "disconnected"
				u.LastError = providerPublicError(probeErr)
			} else {
				u.Status = "connected"
				metrics := probe.metrics
				u.Metrics = &metrics
			}
		} else {
			u.LastError = p.LastError
		}
		return u, probeErr
	})
	if !probed {
		if sessionErr != nil {
			log.Printf("[ProfitOperations] spend refresh skipped provider_id=%d error_type=%T", target.ID, sessionErr)
		}
		return
	}
	if probeErr == nil && probe.spend != nil && sessionErr == nil {
		if err = store.SaveProviderSpend(ctx, service.ProviderSpendSnapshot{
			ProviderID: target.ID, From: from, Until: until, Amount: probe.spend, Currency: probe.spendCurrency, Source: probe.spendSource,
		}); err != nil {
			log.Printf("[ProfitOperations] spend snapshot save failed provider_id=%d error_type=%T", target.ID, err)
		}
		return
	}
	if probeErr != nil {
		if err = store.RecordProviderSpendError(parent, target.ID, from, until, providerPublicError(probeErr)); err != nil {
			log.Printf("[ProfitOperations] spend error save failed provider_id=%d error_type=%T", target.ID, err)
		}
	}
}
