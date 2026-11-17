package service

import "github.com/gin-gonic/gin"

// OpenAIStreamFailedFrame is the in-stream failed or error frame behind a
// failover, kept so an exhausted failover can still apply the error
// passthrough rules that would have matched the frame.
type OpenAIStreamFailedFrame struct {
	Platform string
	Payload  []byte
	Message  string
}

// ApplyOpenAIStreamFailedFrameRule applies the error passthrough rules to the
// frame that triggered an exhausted failover, matching it the way
// applyOpenAIStreamFailedErrorPassthroughRule does for an in-band frame.
func ApplyOpenAIStreamFailedFrameRule(c *gin.Context, failoverErr *UpstreamFailoverError) (status int, errType string, errMsg string, matched bool) {
	if failoverErr == nil || failoverErr.StreamFailedFrame == nil {
		return 0, "", "", false
	}
	frame := failoverErr.StreamFailedFrame
	status, errType, errMsg, matched = applyOpenAIStreamFailedErrorPassthroughRule(c, frame.Platform, frame.Payload, frame.Message)
	if !matched {
		return 0, "", "", false
	}
	if errMsg == "" {
		errMsg = frame.Message
	}
	return status, errType, errMsg, true
}
