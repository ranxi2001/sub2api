//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBatchWhitelistNativePolicyRejectsTaskLifecycle(t *testing.T) {
	for _, task := range []string{"", "existing-task"} {
		t.Run(task, func(t *testing.T) {
			key, privateKey := newTestAgentIdentityKey(t)
			account := newCodexModelsTestAccount()
			account.Credentials["auth_mode"] = OpenAIAuthModeAgentIdentity
			account.Credentials["agent_runtime_id"] = key.runtimeID
			account.Credentials["agent_private_key"] = privateKey
			account.Credentials["task_id"] = task
			repo := &batchWhitelistReadOnlyRepo{upstreamModelMappingRepoStub: &upstreamModelMappingRepoStub{account: account}}
			gateway := newCodexModels401TestService(repo)
			gateway.accountRepo = repo
			_, err := gateway.fetchNativeOpenAIModelsListWithPolicy(context.Background(), account, openAIModelsReadWhitelist)
			require.Error(t, err, "read-only discovery cannot create or recover an identity task")
			require.Zero(t, repo.credentialWrites.Load())
			require.Equal(t, privateKey, account.GetCredential("agent_private_key"))
			require.Equal(t, task, account.GetCredential("task_id"))
		})
	}
}
