package repository

import (
	"context"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Recovery never updates a destination row, regardless of its version or a
// concurrent normal configuration save. Keep UpsertConfig's editing semantics.
func (r *openAIOAuthReauthRepository) InsertTOTPRecoveryConfig(ctx context.Context, config *service.OpenAIOAuthReauthStoredConfig) error {
	result, err := r.db.ExecContext(ctx, `INSERT INTO openai_oauth_reauth_configs
 (account_id,login_email,credential_mode,engine,proxy_source,proxy_id,password_ciphertext,totp_secret_ciphertext,otp_url_ciphertext)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,'')) ON CONFLICT (account_id) DO NOTHING`,
		config.AccountID, config.LoginEmail, config.CredentialMode, config.Engine, config.ProxySource, config.ProxyID,
		config.PasswordCiphertext, config.TOTPSecretCiphertext, config.OTPURLCiphertext)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return infraerrors.Conflict("TOTP_CONFIG_EXISTS", "Recovery import requires an unconfigured destination account")
	}
	return nil
}
