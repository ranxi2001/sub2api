package service

func openAIWSPlaintextBridgeAccount(account *Account) bool {
	return account != nil && (account.IsOpenAIApiKey() || (account.IsOpenAIOAuth() &&
		!account.HasManagedOpenAIConnections() && account.AuthConnectionID == "" &&
		!account.IsSIWCEnabled() && !account.IsCopilotSDKEnabled() && !account.IsExcelOAuth() &&
		!account.IsOpenAIPassthroughEnabled()))
}
