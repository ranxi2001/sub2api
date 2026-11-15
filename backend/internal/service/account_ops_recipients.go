package service

import (
	"errors"
	"net/mail"
	"strings"
)

const maxAccountOpsRecipients = 10

// Keep the existing string field and single-address delivery identities. Lists
// are normalized on save; no recipient address is added to event identity keys.
func parseAccountOpsRecipients(raw string) ([]string, error) {
	if len(raw) > 5120 || strings.ContainsRune(raw, '\r') {
		return nil, errors.New("invalid recipient email list")
	}
	recipients := []string{}
	seen := map[string]bool{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		address, err := mail.ParseAddress(value)
		if err != nil || address.Address != value || len(value) > 254 {
			return nil, errors.New("recipients must contain plain email addresses")
		}
		identity := strings.ToLower(value)
		if !seen[identity] {
			recipients = append(recipients, value)
			seen[identity] = true
		}
	}
	if len(recipients) > maxAccountOpsRecipients {
		return nil, errors.New("at most 10 recipient email addresses are allowed")
	}
	if strings.TrimSpace(raw) != "" && len(recipients) == 0 {
		return nil, errors.New("recipient email list is empty")
	}
	return recipients, nil
}

func (c AccountOpsConfig) emailRecipients() []string {
	recipients, _ := parseAccountOpsRecipients(c.Recipient)
	return recipients
}

func (c AccountOpsConfig) emailRecipient(identity string) string {
	for _, recipient := range c.emailRecipients() {
		if opsEmailIdentity(recipient) == identity {
			return recipient
		}
	}
	return ""
}
