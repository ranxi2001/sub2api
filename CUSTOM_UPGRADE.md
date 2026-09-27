# Custom Sub2API upgrade

Upstream version: v2.8.18
Upstream source commit: c1008182bd1bb8f50ff95133fa486fb9d4676811
Source archive SHA256: 69c3a515e6bd4c8e6a368f348fff33f2e1ce6d0661a03e0ad8e72e65933b4323
Previous official source: e39898c680ecd69381e549ae54c97011107f1757 (v2.8.14).

This repository starts from a verified source-archive import. Its local import
commit is not the original upstream Git commit. Custom changes were ported
from the saved tracked/untracked v2.8.14 deployment snapshot.

Preserved custom functionality:
- Account-controlled image generation bridge and desktop/Lite tool discovery.
- V2 client file delivery with readiness/exit/size/SHA256 checks and V1 receipts.
- Compact image history, local image references and idempotent delivery.
- Relay batch deduplication, expiry headers and opt-in overseas cache prewarm.
- SSE gzip/Brotli/zstd negotiation and nested image concurrency release.

Integration with v2.8.18:
- Retain upstream tool catalog caching, transactional replay and tool repairs.
- Keep server-injected image tools out of shared client catalogs; retain their
  request-scoped permission through native attachment re-preparation.
- Use upstream image-limit settings/UI (1–4096 configurable). Existing custom
  excel_bps_image_max_images_per_request migrates to excel_bps_image_max_images
  only when the canonical key is absent. Keep the old key for rollback.
- Preserve migration checksums; add separate compatibility and Chinese-comment
  migrations following the official 252 schema update.
- Repair the upstream data-import test mocks for i18n/auth dependencies.
- Explicitly initialize the supported-platform fixture in the upstream Mihomo
  subscription-failure test so it exercises the same logic on macOS and Linux.

Relay remains the configured deployment mode. A configurable ceiling is not
a measured production or upstream capacity guarantee. No paid image generation
is required by the local regression suite.

Release gate: production account BPS/bridge flags and existing settings must be
read back before and after deployment. Health alone does not validate image
delivery. Run the existing-image two-turn BPS replay, check actual usage route,
and confirm no new image charge. Do not enable automatic BPS shutdown policies.
