#!/usr/bin/env bash
# Only historical full-production overlays without the provider/QQ/coupon
# implementation omit its later independent test files. The newly merged BPS
# account-search fixture also requires absent historical service symbols.
# Independent BPS settings and provider-team tests also require later APIs.
# GREEN keeps all tests.
# Never remove the selected gateway/moderation/recovery oracle or its fixtures.
set -euo pipefail
baseline="$1"
tree="${2:-.}"
candidate="${3:-$(git -C "$tree" rev-parse HEAD)}"
if test -f "$tree/backend/internal/qqbot/service.go"; then
  exit 0
fi
if git -C "$tree" cat-file -e "$baseline:backend/internal/qqbot/service.go" 2>/dev/null; then
  printf 'refusing mature overlay: baseline contains QQ production\n' >&2
  exit 1
fi
# Prove this is the old financial implementation as well, not a selective
# candidate overlay whose new coupon assertions must continue to compile/run.
proof=backend/internal/service/payment_fulfillment.go
expected=$(git -C "$tree" rev-parse "$baseline:$proof")
test "$(git -C "$tree" hash-object "$proof")" = "$expected"
while IFS= read -r target; do
  if git -C "$tree" cat-file -e "$baseline:$target" 2>/dev/null; then
    printf 'refusing to omit a baseline mature test: %s\n' "$target" >&2
    exit 1
  fi
  expected=$(git -C "$tree" rev-parse "$candidate:$target")
  test "$(git -C "$tree" hash-object "$target")" = "$expected"
  printf 'later independent mature fixture baseline=%s candidate=%s path=%s blob=%s\n' "$baseline" "$candidate" "$target" "$expected"
  rm -- "$tree/$target"
  test ! -e "$tree/$target"
done < <(git -C "$tree" ls-files \
  'backend/internal/qqbot/*_test.go' \
  'backend/internal/coupon/*_test.go' \
  'backend/internal/handler/qq_bot*_test.go' \
  'backend/internal/handler/mature_*_test.go' \
  'backend/internal/handler/admin/mature_*_test.go' \
  'backend/internal/service/qq_bot*_test.go' \
  'backend/internal/service/payment_coupons_test.go' \
  'backend/internal/service/mature_*_test.go' \
  'backend/internal/service/repair_f2_coupon_recovery_test.go' \
  'backend/internal/service/openai_excel_bps_search_test.go' \
  'backend/internal/service/bps_search_settings_test.go' \
  'backend/internal/service/account_ops_providers_test.go' \
  'backend/internal/service/account_ops_team_test.go' \
  'backend/internal/server/routes/mature_*_test.go' \
  'backend/internal/server/routes/repair_r1_payment_review_test.go' \
  'backend/internal/server/routes/repair_provider_session_budget_test.go' \
  'backend/internal/repository/mature_provider_import_integration_test.go' \
  'backend/internal/repository/repair_mature_upgrade_test.go')
