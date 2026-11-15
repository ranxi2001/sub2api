# Provider protocol provenance

The mature_provider_remote, mature_provider_newapi_auth, mature_provider_session,
mature_provider_key_remote, and mature_provider_key_types files adapt the
corresponding provider_*.go files from mizaawa/zayuapi v0.2.219,
a71aa9615ccb55a19973908c9f261b5ba25a48c6, under the repository LGPL v3 license.
Upstream authors retain their copyrights. Existing source notices are retained.
Source: https://github.com/mizaawa/zayuapi/tree/a71aa9615ccb55a19973908c9f261b5ba25a48c6/backend/internal/handler/admin

This fork replaces whole-setting persistence and process-local locks with SQL
revision checks and row locks. Protocol requests use public IP pinning, no
redirects or environment proxies, and separate cookie jars. Full per-file
provenance is recorded in docs/mature-provider-provenance.json.
