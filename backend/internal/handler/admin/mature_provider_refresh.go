package admin

import (
	"context"
	"time"
)

func (h *ChannelHandler) startMatureProviderRefresh() {
	ctx, cancel := context.WithCancel(context.Background())
	h.providerCancel = cancel
	h.providerWG.Add(1)
	go func() {
		defer h.providerWG.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for n := 0; n < 10; n++ {
					claimed, err := h.RefreshDueMatureProvider(ctx)
					if err != nil || !claimed {
						break
					}
				}
			}
		}
	}()
}

func (h *ChannelHandler) StopMatureProviderRefresh() {
	if h.providerCancel != nil {
		h.providerCancel()
	}
	h.providerWG.Wait()
}

func (h *ChannelHandler) RefreshDueMatureProvider(parent context.Context) (bool, error) {
	r, err := h.channelService.MatureProviderRepository()
	if err != nil {
		return false, err
	}
	id, revision, err := r.ClaimMatureProviderRefresh(parent)
	if err != nil || id == 0 {
		return false, err
	}
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	_, err = h.refreshMatureProvider(ctx, id, revision)
	return true, err
}
