//go:build unit

package service

import "time"

// SetOpenAIRetryDeadlineForTest seeds the existing account retry window without
// changing its production duration or the context carried by the real handler.
func SetOpenAIRetryDeadlineForTest(s *OpenAIGatewayService, accountID int64, deadline time.Time) {
	s.openaiOAuth429RetryStartedAt.Store(accountID, deadline.Add(-openAIOAuth429RetryWindow))
}

// OpenAIRetryPoolLoadForTest observes lease cleanup through the pool's lock.
func OpenAIRetryPoolLoadForTest(s *OpenAIGatewayService, accountID int64) (int, int, int) {
	return s.getOpenAIWSConnPool().AccountPoolLoad(accountID)
}
