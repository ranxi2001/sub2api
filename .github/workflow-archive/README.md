# 历史 CI 归档

2026-11-17 从 `.github/workflows/` 迁出 32 份工作流。GitHub Actions 不加载本目录；文件原样保留，便于追溯旧 PR 的验收命令、固定基线和截图步骤。测试源码及取证脚本仍在原位置。

归档基线：`e3f238e3f`（production）。归档对象只包含已合并功能的专用分支验收、旧上游版本同步、历史截图和试用构建；当前开放 PR 的专用入口保留；另外归档仅允许在 `Wei-Shaw/sub2api` 执行的上游 CLA 工作流，它在本 fork 不提供检查。主 CI 继续执行后端 unit/integration、前端关键测试、lint、部署及发布辅助脚本检查。

| 工作流 | 历史关联 |
| --- | --- |
| [cla.yml](cla.yml) | 上游专用 CLA；本 fork 的两个 job 均被仓库条件排除 |
| [account-connection-timing.yml](account-connection-timing.yml) | [#602](https://github.com/ranxi2001/sub2api/pull/602) |
| [account-ops-selector.yml](account-ops-selector.yml) | [#837](https://github.com/ranxi2001/sub2api/pull/837) |
| [admin-usage-platform-filter.yml](admin-usage-platform-filter.yml) | [#836](https://github.com/ranxi2001/sub2api/pull/836) |
| [http2-stream-reset.yml](http2-stream-reset.yml) | [#833](https://github.com/ranxi2001/sub2api/pull/833)、[#846](https://github.com/ranxi2001/sub2api/pull/846) |
| [issue-582-regression.yml](issue-582-regression.yml) | [#592](https://github.com/ranxi2001/sub2api/pull/592) |
| [issue603-observability.yml](issue603-observability.yml) | [#604](https://github.com/ranxi2001/sub2api/pull/604) |
| [issue605-p1-repair.yml](issue605-p1-repair.yml) | [#606](https://github.com/ranxi2001/sub2api/pull/606) |
| [issue605-smart-protocol.yml](issue605-smart-protocol.yml) | [#606](https://github.com/ranxi2001/sub2api/pull/606) |
| [issue811-acl-followup.yml](issue811-acl-followup.yml) | [#828](https://github.com/ranxi2001/sub2api/pull/828) |
| [issue825-video-pricing.yml](issue825-video-pricing.yml) | [#826](https://github.com/ranxi2001/sub2api/pull/826) |
| [issue881-totp.yml](issue881-totp.yml) | [#882](https://github.com/ranxi2001/sub2api/pull/882) |
| [mature-channel-community-ops.yml](mature-channel-community-ops.yml) | [#850](https://github.com/ranxi2001/sub2api/pull/850) |
| [mature-provider-scope-proof.yml](mature-provider-scope-proof.yml) | [#850](https://github.com/ranxi2001/sub2api/pull/850) |
| [mature-provider-session-repair.yml](mature-provider-session-repair.yml) | [#850](https://github.com/ranxi2001/sub2api/pull/850) |
| [mature-review-repairs.yml](mature-review-repairs.yml) | [#850](https://github.com/ranxi2001/sub2api/pull/850) |
| [moderation-upstream-scope.yml](moderation-upstream-scope.yml) | [#848](https://github.com/ranxi2001/sub2api/pull/848) |
| [oauth-fallback-scope.yml](oauth-fallback-scope.yml) | [#797](https://github.com/ranxi2001/sub2api/pull/797) |
| [oauth-initial-ws-screenshots.yml](oauth-initial-ws-screenshots.yml) | [#749](https://github.com/ranxi2001/sub2api/pull/749) |
| [pelican-trial-build.yml](pelican-trial-build.yml) | `feat/pelican-schedule` 历史试用二进制构建 |
| [pool-capacity-regression.yml](pool-capacity-regression.yml) | [#569](https://github.com/ranxi2001/sub2api/pull/569) |
| [pr596-cooldown-ownership.yml](pr596-cooldown-ownership.yml) | [#596](https://github.com/ranxi2001/sub2api/pull/596) |
| [pr597-stream-timing.yml](pr597-stream-timing.yml) | [#597](https://github.com/ranxi2001/sub2api/pull/597)、[#599](https://github.com/ranxi2001/sub2api/pull/599) |
| [pr610-queue-timing.yml](pr610-queue-timing.yml) | [#610](https://github.com/ranxi2001/sub2api/pull/610) |
| [pr838-compat-count.yml](pr838-compat-count.yml) | [#838](https://github.com/ranxi2001/sub2api/pull/838) 的兼容性计数补修分支 |
| [qwen-platform.yml](qwen-platform.yml) | [#798](https://github.com/ranxi2001/sub2api/pull/798) |
| [request-timing-build.yml](request-timing-build.yml) | [#36](https://github.com/ranxi2001/sub2api/pull/36)、[#37](https://github.com/ranxi2001/sub2api/pull/37) |
| [twofa-import-parallel.yml](twofa-import-parallel.yml) | [#787](https://github.com/ranxi2001/sub2api/pull/787) |
| [upstream-v0219-regression.yml](upstream-v0219-regression.yml) | [#590](https://github.com/ranxi2001/sub2api/pull/590) |
| [upstream-v0220-regression.yml](upstream-v0220-regression.yml) | [#722](https://github.com/ranxi2001/sub2api/pull/722) |
| [upstream-v0221-regression.yml](upstream-v0221-regression.yml) | [#789](https://github.com/ranxi2001/sub2api/pull/789) |
| [upstream-v0221-ui.yml](upstream-v0221-ui.yml) | [#789](https://github.com/ranxi2001/sub2api/pull/789) |

主 CI 同时移除已合并分支的 `issue854-behavior`、`pelican-round-evidence`、`pr612-regressions`、`pr612-regressions-red` 以及质量模板、用户启用和 Issue854 的条件步骤；VIP 入账工作流也清除旧分支的两段 RED 步骤和 `source-format` job，保留 PostgreSQL GREEN 回归。原定义可从上述归档基线的 `.github/workflows/backend-ci.yml` 查阅。

上述 32 个入口已在 GitHub 登记为停用，历史运行记录保留。需要重跑历史验收时，在任务分支恢复所需文件到 `.github/workflows/`，先核对固定 SHA、分支条件、工具链、runner 和脚本路径，再按任务范围重新启用对应工作流。归档文件保留当时配置，不保证在新代码上直接可运行。常规回归优先并入主 CI；确需专用工作流时按相关路径触发，临时 RED/GREEN、截图或试用构建在任务完成后归档。

## Issue 985 Anthropic SSE integrity

[anthropic-stream-integrity-red-green.yml](anthropic-stream-integrity-red-green.yml)
retains the temporary fixed-production `dfcc6623` RED/GREEN workflow used by
[PR #987](https://github.com/ranxi2001/sub2api/pull/987). The immutable oracle ran
against the original checkout and clean candidates, with raw identity/test logs
in [run 44907259566](https://github.com/ranxi2001/sub2api/actions/runs/44907259566).
That run passed the strict RED/GREEN validator, race, real handlers, official SDK
accumulation, isolated PostgreSQL billing and ordinary server build.

The active `anthropic-stream-integrity.yml` retains the reusable path-triggered
GREEN/race/SDK/PostgreSQL/build gate and validator negative controls. Unit tests
also remain selected by main CI, whose integration job explicitly runs the PG
and official SDK fixtures. Archiving the fixed baseline does not mark PR review
or the current full CI complete; the PR stays Draft pending those gates.
