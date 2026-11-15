package qqbot

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestSpamReportSourceDiagnosticRepliesAreActionableAndBounded(t *testing.T) {
	for _, reason := range []spamReportSourceReason{
		spamSourceNotFound, spamSourceExpired, spamSourceAmbiguous, spamSourceWhitelist,
		spamSourceProcessingExpired, spamSourceProcessingInsufficient, spamSourceReportInvalid, spamSourceReportMissing,
		spamSourceWhitelistChanged, spamSourceChatAuthorizationChanged, spamSourcePolicyDisabled, spamSourceChanged,
		spamSourceChatAuthorized, spamSourceQQAdmin, spamSourceBot, spamSourceRoleMissing,
		spamSourceRoleChanged, spamSourcePolicyInactive, spamSourcePolicyChanged,
		spamSourceIneligible, spamSourceConflict, spamSourceMentionOnly, spamSourceLegacy,
		spamSourceInvalidTime, spamSourceInvalidRef, spamSourceUnverified,
	} {
		t.Run(string(reason), func(t *testing.T) {
			require.Contains(t, reason.decision(), string(reason))
			require.Contains(t, reason.decision(), "未调用模型、未执行处罚")
			require.LessOrEqual(t, utf8.RuneCountInString(reason.decision()), 240)
			require.LessOrEqual(t, utf8.RuneCountInString(reason.decisionWithModel(true)), 240)
			require.NotContains(t, reason.decisionWithModel(true), "未调用模型")
			require.Contains(t, reason.reply(), "请")
			require.Contains(t, reason.reply(), "本次不撤回、不禁言")
		})
	}
	require.Contains(t, spamSourceNotFound.reply(), "不能据此认定权限不足")
	require.NotContains(t, spamSourceNotFound.reply(), "没有收到")
	require.Contains(t, spamSourceOK.decision(), string(spamSourceUnverified))
}

func TestSpamReportCancellationReasonOnlyDisplaysKnownFixedCodes(t *testing.T) {
	for _, reason := range []spamReportSourceReason{spamSourceWhitelistChanged, spamSourceChatAuthorizationChanged, spamSourcePolicyDisabled, spamSourcePolicyChanged, spamSourceProcessingExpired} {
		require.Equal(t, reason, spamReportCancellationReason(reason.audit()))
		require.Equal(t, reason, spamReportCancellationReason(string(reason)))
		require.NotContains(t, spamReportCancellationReason(string(reason)+"：private-model-output").reply(), "private-model-output")
	}
	require.Equal(t, spamSourceReportInvalid, spamReportCancellationReason("private-unknown-reason"))
}
