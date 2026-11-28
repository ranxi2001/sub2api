# Configure Resend for email delivery

Sub2API keeps SMTP as the default email provider. Administrators can select Resend in **Admin → Settings → Email** to send verification, password reset, and notification messages through Resend without operating an SMTP server.

## Before switching

1. Create an API key in the Resend dashboard with permission to send email. Store it securely until it has been entered in Sub2API.
2. Add and verify the sending domain with Resend. The `From Email` address in Sub2API must use that verified domain. Resend's unverified testing mode only permits sending to the account's own address.
3. Ensure the server's configured `TOTP_ENCRYPTION_KEY` is persistent and available to every application instance. Sub2API encrypts the Resend API key with the existing AES-256-GCM settings secret encryptor. Losing or rotating this key without re-entering the provider key makes the saved Resend key unreadable.

## Save and send a test message

1. Open **Admin → Settings → Email** and choose **Resend**.
2. Enter the Resend API key, `From Email`, and optional `From Name`, then save settings.
3. The settings response only reports whether a key is configured. It never returns the key. Leave the password field empty on later saves to retain the current key; entering a replacement rotates it.
4. Enter a recipient in **Send Test Email** and send a test message. Resend delivery requires the sender domain verification and account limits to permit that recipient.

Switching the provider back to SMTP retains the saved Resend key. SMTP fields and the existing SMTP TLS behavior remain unchanged. Resend requests use the fixed official `https://api.resend.com/emails` endpoint over normal TLS, with a 15-second timeout and no automatic retry or provider fallback. Provider response bodies are not included in application errors.

A test email intentionally sends a real message when used in a configured deployment. Use a controlled recipient and avoid repeated sends.
