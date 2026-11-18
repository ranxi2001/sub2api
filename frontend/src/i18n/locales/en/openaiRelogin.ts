export default {
  "twoFA": {
  "title": "2FA login and import",
  "label": "Initial login with email, password and 2FA",
  "hint": "One email----password----2FA per line (commas also supported), up to 100 entries, processing at most 3 accounts at a time. Initial login uses the configured login service. Imported accounts join Credential Operations by default with encrypted password/2FA storage, inspection and automatic re-login enabled. Subsequent logins use the local Worker. Enable credential encryption on this page; automatic re-login also requires a configured Worker. Stopping aborts page requests and further processing; remote logins or saves already submitted may still complete.",
  "settings": "View relogin service settings",
  "credentials": "Login credentials",
  "placeholder": "email----password----2FA secret",
  "nameHint": "Leave the name empty to use the email; otherwise it is used as a prefix. Click Next to enter login credentials.",
  "invalid": "Enter 1–100 complete email, password and 2FA entries, one per line with no duplicate emails.",
  "start": "Log in and import",
  "retry": "Continue unfinished entries",
  "stop": "Stop this batch",
  "ownedJobs": "Some login tasks are unresolved, so this window must stay open. Continue unfinished entries to resume them, or stop this batch to clean them up. If cleanup fails, you can try again.",
  "reset": "Import another batch",
  "states": {
    "pending": "Pending",
    "login": "Logging in…",
    "importing": "Importing and enrolling in Credential Operations…",
    "created": "Imported and enrolled in Credential Operations",
    "skipped": "Existing account enrolled in Credential Operations",
    "failed": "Login incomplete; retry or check credentials and the initial login service",
    "importFailed": "Logged in; import or Credential Operations enrollment incomplete. Check encryption settings and retry"
  }
}
}
