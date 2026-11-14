package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strconv"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

const connectionAuthorizationReceipt = "_authorization_session_hash"
const connectionAuthorizationTime = "_authorization_saved_at"

func connectionAuthorizationHash(sessionID string) string {
	digest := sha256.Sum256([]byte("connection-authorization\x00" + sessionID))
	return hex.EncodeToString(digest[:])
}

type OpenAIConnectionAuthorizationStatus struct {
	Status string `json:"status"` // waiting, saved, or unconfirmed; never token/code/state
}

// ConnectionAuthorizationStatus never exchanges a code or changes a connection.
// A receipt is encrypted and committed with credentials in the same CAS. A
// revision change alone cannot prove that this authorization was saved.
func (s *OpenAIOAuthReauthService) ConnectionAuthorizationStatus(ctx context.Context, accountID int64, id, sessionID string) (*OpenAIConnectionAuthorizationStatus, error) {
	if _, err := s.managementConnectionAccount(ctx, accountID); err != nil {
		return nil, err
	}
	if _, err := siwcSessionKey(sessionID); err != nil {
		return nil, infraerrors.BadRequest("OPENAI_CONNECTION_SESSION_INVALID", "Invalid authorization session")
	}
	c, err := s.authConnection(ctx, accountID, id)
	if err != nil {
		return nil, err
	}
	result := &OpenAIConnectionAuthorizationStatus{Status: "unconfirmed"}
	if c.Legacy || c.Kind == "api_key" {
		return result, nil
	}
	creds, err := s.readConnectionCredentials(c)
	if err != nil {
		return nil, err
	}
	savedAt, _ := strconv.ParseInt(stringCredential(creds, connectionAuthorizationTime), 10, 64)
	age := time.Since(time.Unix(savedAt, 0))
	if c.Status == "active" && age >= 0 && age <= openai.SessionTTL &&
		subtle.ConstantTimeCompare([]byte(stringCredential(creds, connectionAuthorizationReceipt)), []byte(connectionAuthorizationHash(sessionID))) == 1 {
		result.Status = "saved"
		return result, nil
	}
	if session, ok := s.readSIWCSession(ctx, sessionID, false); ok && session.SIWCAccountID == accountID &&
		session.ConnectionID == id && session.ConnectionKind == c.Kind && session.ConnectionRevision == c.Revision {
		result.Status = "waiting"
	}
	// Missing, consumed, expired and unavailable sessions cannot prove failure
	// or success. Leave the result unconfirmed and never suggest code replay.
	return result, nil
}
