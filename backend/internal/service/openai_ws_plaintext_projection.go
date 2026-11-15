package service

// Minted only by the HTTP bridge from its reconstructed current-turn input,
// before request builders. It is neither a retry grant nor portable ownership.
type openAIWSPlaintextProjectionKey struct{}
type openAIWSPlaintextProjection struct {
	anchor                                       openAIWSBridgeResponseAnchor
	accountID                                    int64
	previous                                     string
	sourceInput, sourceControls, input, controls [32]byte
	history                                      []byte
}

// Exact completed response and principal whose history this bridge holds.
type openAIWSBridgeResponseAnchor struct {
	response, model, session  string
	account, key, user, group int64
	config                    [32]byte
}

func openAIWSBridgeAnchor(request *openAIAgentRecoveryRequest, account *Account, response string) openAIWSBridgeResponseAnchor {
	return openAIWSBridgeResponseAnchor{response: response, model: request.model, session: request.session,
		account: account.ID, key: request.keyID, user: request.userID, group: request.groupID,
		config: openAIAgentRecoveryConfig(account, request.model)}
}

// This proof never leaves the active manager or authorizes another account.
type openAIWSDeliveredTool struct {
	fingerprint string
	accountID   int64
	config      [32]byte
	conflicted  bool
}
