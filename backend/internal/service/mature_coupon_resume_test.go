package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatureCouponSurvivesSignedPaymentResume(t *testing.T) {
	s := NewPaymentResumeService([]byte("synthetic-signing-key-32-bytes-long"))
	var claims WeChatPaymentResumeClaims
	require.NoError(t, json.Unmarshal([]byte(`{"openid":"synthetic-openid","pt":"wxpay","amt":"100","ot":"balance","coupon_id":7}`), &claims))
	token, err := s.CreateWeChatPaymentResumeToken(claims)
	require.NoError(t, err)
	resumed, err := s.ParseWeChatPaymentResumeToken(token)
	require.NoError(t, err)
	raw, err := json.Marshal(resumed)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))
	require.EqualValues(t, 7, fields["coupon_id"], "signed payment resumption must preserve the selected coupon")
	require.Equal(t, "wechat_payment_resume_coupon", fields["tk"], "older binaries must reject a coupon flow instead of dropping its benefit")
}
