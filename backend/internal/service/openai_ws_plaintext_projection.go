package service

// Minted only by the HTTP bridge from its reconstructed current-turn input,
// before request builders. It is neither a retry grant nor portable ownership.
type openAIWSPlaintextProjectionKey struct{}
type openAIWSPlaintextProjection struct {
	accountID                                    int64
	previous                                     string
	sourceInput, sourceControls, input, controls [32]byte
	history                                      []byte
}

// This proof never leaves the active manager or authorizes another account.
type openAIWSDeliveredTool struct {
	fingerprint string
	accountID   int64
	config      [32]byte
	conflicted  bool
}
