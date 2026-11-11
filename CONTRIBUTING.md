# 贡献指南

本指南适用于 [ranxi2001/sub2api](https://github.com/ranxi2001/sub2api)。欢迎提交可复现的 Bug、功能建议、文档修正、测试和代码改进。中文或英文均可，保留错误消息、字段名和命令的原文。

本 fork 以 `production` 为默认开发和 PR 目标分支，按需引入 [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api) 的更新。提交前核实仓库与 base 分支，避免把 PR 发到上游 `main`。运维 CLI、监控及生产部署脚本属于 [sub2api-operate](https://github.com/ranxi2001/sub2api-operate)，应用源码改动提交到本仓库。

## Agent 使用建议与披露

**强烈建议使用 Astra 或 Opus 5.5 编写 PR。Fable 系列及其他模型也可以提交。** 这是使用建议，不是模型准入限制。

所有 Issue 和 PR 都必须注明 **Agent 名称、模型名称和推理强度**，无论使用推荐模型、其他模型，还是完全人工编写，方便审核者判断内容的可信度和需要重点核查的部分。仅用 Agent 分析问题、编写测试、撰写或润色正文，也需要披露。

- **Agent 名称与参与范围**：填写实际使用的工具名称，以及参与的问题分析、代码、测试、文档或正文撰写工作。纯人工编写注明“未使用 Agent，参与范围：无”。
- **模型名称**：填写实际使用的完整模型名称或模型 ID，不能只写工具名或“AI”。纯人工编写填写“不适用”。
- **推理强度**：填写实际配置值。未手动设置时写“默认（未手动设置）”；工具不提供该设置时写“不适用（工具未提供）”；纯人工编写填写“不适用”。
- 多个 Agent 或模型参与时，包括子 Agent，逐一列出各自的模型、推理强度和参与范围；后续追加修改时同步更新声明。
- 模型名称或推理强度无法获取时，明确注明“未知”及原因，不凭输出风格猜测，不把未确认的默认值写成具体档位。

使用推荐模型不能替代验证。贡献者仍需核对实现、依赖、命令和证据，说明实际执行的检查、结果及未验证部分，不得将 Agent 的判断当作已验证的事实。

## 选择合适的入口

从 [Issue 选择页](https://github.com/ranxi2001/sub2api/issues/new/choose) 选择表单：

| 类型 | 适用情况 | 重点提供 |
| --- | --- | --- |
| Bug 反馈 | 异常响应、页面错误、错误计费、调度或传输异常 | 实际构建、前置条件、步骤、预期/实际结果、脱敏证据 |
| 功能建议 | 缺少能力或现有流程难以满足需求 | 使用场景、最小范围、验收条件、替代方案 |
| 文档问题 | 命令失效、步骤遗漏、链接或说明错误 | 文档位置、适用版本、操作过程、预期修正 |
| 使用与配置问题 | 不确定行为含义或如何配置，尚不能判断是否为 Bug | 操作目标、版本环境、已查资料、尝试与结果 |

先搜索开放和已关闭的 Issue。已有相同问题时，在原 Issue 补充新版本、复现样例或对照结果；只有错误文字相同、尚未确认根因一致时，说明差异并关联。一个 Issue 尽量对应一个可独立验收的问题；汇总事件时将不同症状和待办分开。大功能、破坏性改动、协议行为或迁移方案建议先讨论范围，小型修复可直接提交 PR。

表单不会要求先升级生产、交出账号、提供完整对话或证明根因。未知项写“未知”，不适用项写“不适用”；不能复现、没有管理权限或不能公开样例时，说明缺失原因及可提供的替代证据。

## Bug 报告：让其他人能复现

### 1. 固定实际运行环境

- **构建身份**：版本号、Release tag，或镜像 tag 与 digest；自编译/试用构建补充实际 SHA、基线、关联 PR 及本地修改。不要只写 `latest`。可执行实际运行的二进制 `-version` 查看信息；缺少 commit 时写“未记录”，不要用当前源码 HEAD 代替。
- **客户端与服务端**：部署方式、操作系统/架构、客户端/SDK/插件与版本；页面问题补充浏览器和入口页面。
- **请求条件**：端点、模型原名与映射名、平台、账号类型、流式开关、工具或媒体类型、会话是否包含历史；通道无法确认时写“未确认”。
- **配置与网络**：只列影响问题的开关、超时、并发、CDN/反代/业务代理路径。涉及 ticket 或 Mihomo 时区分采集出口和业务出口。说明配置快照采集时间，避免把排查时状态当成故障时状态。

可以在准备报告时记录以下信息；这些命令只描述当前源码或工具链，不能证明正在运行哪个构建：

```bash
git rev-parse HEAD
git status --short
go version
node --version
pnpm --version
```

### 2. 从明确的前置条件写步骤

按“准备状态 → 输入/操作 → 观察位置 → 预期与实际结果”描述，让维护者不用猜账号类型、分组、开关或客户端行为。API 问题尽量提供最小请求、可运行的测试或离线 mock；页面问题写清角色、页面路径、操作顺序及虚构测试数据。

1. 保留触发问题所需的配置和输入，一次只删减一类无关字段。
2. 条件允许时确认删减后仍复现，并记录成功/失败次数。没有复测就写“样例已脱敏，尚未验证删减后能否复现”。
3. 工具调用问题保留工具定义、字段类型、顺序、tool call 与 output 的对应关系。重复或冲突定义本身可能是触发条件，不要先去重或改名。
4. 长上下文问题提供构造方式、轮数、输入字节数和 token 估算来源；媒体问题提供可公开的虚构素材、格式与大小。不要上传真实对话或私人图片。
5. 偶发问题记录观察窗口、尝试次数、成功对照和失败记录。仅有历史证据时写原操作与复现障碍，不要求反复重放真实请求。

下面是**收集最小请求证据的操作示例**，不是任何现有 Issue 的已验证复现。它会发送一次请求，可能产生费用；仅在自己的隔离测试实例和获准测试的账号中执行。协议、模型和请求体应换成与问题一致的最小样例。不要对已发送或可能产生副作用的请求自动重试。

在 Bash 中逐段执行；关闭命令跟踪，凭据只写入权限受限的临时文件。先把请求里的 `MODEL_ALIAS` 改成测试模型，再执行最后一段：

```bash
set +x
umask 077
repro_dir="$(mktemp -d)"
cat > "$repro_dir/request.json" <<'JSON'
{
  "model": "MODEL_ALIAS",
  "input": "只回复 OK",
  "stream": true
}
JSON

# 编辑上面生成的 request.json，保留与问题相关的最小字段。
# 使用实际测试端点；不要将凭据放进 URL。
SUB2API_ENDPOINT='http://127.0.0.1:8080/v1/responses'
read -r -s -p '测试 API Key: ' SUB2API_KEY
printf '\n'
printf 'Authorization: Bearer %s\n' "$SUB2API_KEY" > "$repro_dir/request.headers"
unset SUB2API_KEY
```

```bash
date -u '+%Y-%m-%dT%H:%M:%SZ' > "$repro_dir/timing.txt"
curl_status=0
curl --silent --show-error --no-buffer \
  --connect-timeout 10 --max-time 120 \
  --header @"$repro_dir/request.headers" \
  --header 'Content-Type: application/json' \
  --data-binary @"$repro_dir/request.json" \
  --dump-header "$repro_dir/response.headers" \
  --output "$repro_dir/response.body" \
  --write-out 'http_code=%{http_code}\ntime_starttransfer_s=%{time_starttransfer}\ntime_total_s=%{time_total}\n' \
  "$SUB2API_ENDPOINT" >> "$repro_dir/timing.txt" \
  2> "$repro_dir/curl.stderr" || curl_status=$?
printf 'curl_exit_code=%s\n' "$curl_status" >> "$repro_dir/timing.txt"
rm -- "$repro_dir/request.headers"
printf '证据保存在 %s；人工脱敏后只分享必要片段。\n' "$repro_dir"
```

`--max-time 120` 是本示例主动设置的客户端时限，可能触发客户端取消；报告中应保留这个条件，按原问题调整时记录实际值。`curl_exit_code=0` 不代表 HTTP 或业务成功；`time_starttransfer` 是 curl 的首字节计时，不等于模型首 token 时间。响应头也可能含 Cookie 或敏感标识，临时目录不能整包上传；若中途退出，需自行删除 `request.headers`，材料用完后清理目录。

### 3. 用同一请求关联证据

优先给出日期、时间、时区及允许公开的完整 request ID。客户端 ID、网关 request ID、上游 ID 和重试尝试号可能不同，分别注明名称、来源和对应关系。需要隐藏的内部 ID 使用一致别名，并说明维护者无法凭别名直接查线上记录。

下面的表可以复制到 Bug 表单；一行对应一条请求或一次尝试，缺失字段写“未采集/无权限/不适用”，不要填猜测值：

| 时间（含时区） | request ID / 尝试号 | 端点、模型、通道 | 客户端/网关 HTTP 状态 | 上游状态或业务错误 | 耗时、单位与来源 | 证据来源 |
| --- | --- | --- | --- | --- | --- | --- |
| 待填写 | 待填写 | 待填写 | 待填写 | 待填写 | 待填写 | 待填写 |

- 贴同一请求附近的少量脱敏客户端日志、访问日志、错误记录或 SSE 事件，保留时间、事件名、错误码和关联字段；原始文本便于检索，截图只作补充。
- 流式请求要分别记录 HTTP 状态与最终业务结果、是否收到首字节/事件、最后事件及是否正常终止；流已经开始后的失败不能只凭 HTTP 200 判断成功。
- 区分超时、上游 HTTP 拒绝、EOF、客户端取消和请求校验失败。记录 `error_kind`、`phase`、`request_written` 等字段时标明来源；未采集不能补成 `false`。
- 分别记录 `duration_ms`、`latency_ms`、首字节或客户端总耗时的单位与口径。`NULL`、页面 `-` 与 `0` 不等价，也不要把不同来源计时直接当成同一指标。
- 报告“失败率”必须有时间窗口、分母和重试口径；只核对部分记录时写明样本范围。账号当前可用不能证明故障时可用，同样，某次 400 或客户端取消也不足以认定账号失效。

需要采集请求时先看 [请求采集说明](docs/request-capture.md)，仅采集获准测试的请求，控制范围并人工检查导出内容。不要为提交 Issue 随意开启全站正文日志。

### 4. 把已知事实和推测分开

建议按“已观察 / 已验证 / 待验证 / 无法验证的原因”记录排查。对照实验一次改变一个条件，例如客户端版本、stream 开关、代理或通道；记录操作和结果，不要将一次重试成功写成已修复。

[Issue #141](https://github.com/ranxi2001/sub2api/issues/141) 可参考其证据组织方式：记录实际构建、带时区的请求明细、日志关联和证据范围，并将未确定的原因单独说明。它是一份历史事件排查记录，不能直接当作现成的最小复现；提交类似问题仍需要补充可获得的触发条件和样例。

## 脱敏与安全报告

### 公开内容检查

正文、代码、提交历史、截图、日志和附件都要检查：

- 删除 API Key、Authorization、Cookie、access/refresh token、OAuth 导出、Recovery Ticket、ticket/state、CDK、签名下载地址以及代理和订阅 URL 中的密钥。
- 移除账号邮箱、个人身份、真实 IP、客户数据、对话和私人媒体。不要直接上传完整 HAR、数据库、账号导出、`config.yaml` 或 `.env`。
- 用虚构值替换数据，保留复现依赖的类型、结构、长度特征和 ID 关系。不要把同一个 ID 在不同记录中替换成不同值。
- 手工复查自动脱敏结果。发现凭据已公开时先撤销/轮换，再移除公开内容；编辑 Issue 或删除文件不等于凭据从历史记录中消失。

### 安全漏洞私密报告

涉及认证绕过、越权、密钥泄露或可利用漏洞时，不要在公开 Issue/PR 放利用步骤或凭据。如仓库 Security 页面提供 **Report a vulnerability**，使用该私密入口；如果未启用，通过 [README 的社区联系方式](README.md#社区交流群) 联系维护者约定私密渠道，初次联络只说明需要报告安全问题，不在群内公开细节。普通功能 Bug 使用 Issue 表单。

## 准备代码贡献

### 分支与范围

Fork 本仓库后，下面示例中的 `origin` 是自己的 fork，`project` 指向本项目。已有 checkout 先检查 `git remote -v`；不要覆盖已有远端或未提交修改。

```bash
# 将 YOUR_GITHUB_LOGIN 替换为自己的 GitHub 用户名。
git clone https://github.com/YOUR_GITHUB_LOGIN/sub2api.git
cd sub2api
git remote add project https://github.com/ranxi2001/sub2api.git
git fetch project production
git switch -c fix/describe-the-change project/production
```

每个 PR 聚焦一个问题。说明请求路径、账号类型、配置默认值、兼容性和用户可见变化。协议、调度、计费、权限、重试和数据库改动需要对应的边界验证；不要顺手调整无关行为或格式。

引入上游/其他 fork 的代码时附来源链接与原 SHA，先确认是否已有等价补丁；按提交引入时优先使用 `git cherry-pick -x` 保留来源。不要用上游 tag 覆盖本项目生产分支，也不要把来源 merge commit 和其实际功能提交重复引入。

### 新功能提示登记与接入

本节是管理员新功能提示的完整贡献规范；开发指南和 PR 模板提供审查摘要，以本节为准。这是规范和审查清单，沿用 [PR #622](https://github.com/ranxi2001/sub2api/pull/622) 的现有机制，不新增运行时功能。

新增管理员可见的功能、菜单、页签或设置项时，必须在同一个 PR 中完成以下事项，不能留到 Release 发布后补录：

- 在[提示 ID 登记文件](frontend/src/constants/newFeatureNotices.ts)登记新的稳定且唯一的提示 ID，并在对应功能的所有展示位置接入 `NewFeatureBadge`；组件用法见[接入说明](frontend/src/components/common/README.md#newfeaturebadgevue)。
- 同一功能有多个入口时共用同一个 ID，点击任一标签后相关入口的提示同步隐藏；不同新功能使用不同 ID，避免关闭旧提示时连带隐藏新功能提示。
- 普通修复、文案调整和重复发版不更换已有 ID。同一功能确需重新通知管理员时，明确作为一次新公告登记新 ID，并在 PR 中说明重新公告的原因。
- PR 说明必须列出提示 ID、所有展示位置和关闭行为，并在同一个 PR 中补充必要的回归验证，记录操作或命令、实际结果及未覆盖的情况。不涉及管理员可见新功能时，相关字段和清单逐项填写“不适用 + 原因”。

审查时按以下顺序验收，不把填写清单当成已经执行运行时测试：

1. 管理员已关闭功能 A 的提示；升级到包含功能 B 的版本后，B 使用新 ID，在对应位置仍可见，A 保持隐藏。
2. B 有多个入口时，点击任一入口的标签，所有相关入口的提示同步隐藏。
3. 在同一浏览器中刷新页面或重新登录同一管理员账号，B 仍保持隐藏；前提是 `localStorage` 可用且未被清除。
4. 后续功能 C 登记新 ID 后正常显示；普通升级不会让 A/B 已关闭的提示重新出现。

隐藏记录的边界是当前浏览器、当前管理员的 `localStorage`；不自动解析 Release 版本或发布说明，不承诺跨浏览器或服务端同步。新功能 PR 应提供上述验收的回归证据；纯规范文档 PR 应说明未执行运行时测试及原因。

### 环境与依赖

工具链以当前 checkout 的 [backend/go.mod](backend/go.mod)、[CI](.github/workflows/backend-ci.yml)、[frontend/package.json](frontend/package.json) 和锁文件为准。本指南编写时，后端 CI 使用 Go 1.27.4 与 golangci-lint v2.14.0；前端要求 Node.js >=22，CI 使用 Node.js 22 和 pnpm 9，与锁定的 vue-i18n / @intlify/message-compiler 11.4.16（Node.js >=22）一致，frontend/package.json 的 engines 与所有前端工作流保持一致；Docker 前端构建阶段使用 Node.js 24。版本要求变化时一并更新说明，不沿用旧教程中的最低版本。

运行应用需要独立的 PostgreSQL/Redis 测试环境，配置与初始化方式见 [部署说明](deploy/README.md)。部分集成测试通过 testcontainers 启动依赖，需要可用的 Docker；缺少 Docker 或测试环境变量可能导致用例跳过，检查日志中的 skip 信息，不能仅凭退出码声称全部覆盖。

从仓库根目录安装依赖：

```bash
pnpm --dir frontend install --frozen-lockfile
(cd backend && go mod download)
```

配置仅放在本地，使用虚构数据，确保不会连接生产数据库或缓存。应用启动可能执行数据库迁移，不要用生产数据验证开发改动。配置好测试环境后，在两个终端分别执行：

```bash
# 终端 1：后端。go run 不提供自动热重载。
cd backend
go run ./cmd/server
```

```bash
# 终端 2：从仓库根目录执行，前端默认开发端口为 3000。
pnpm --dir frontend run dev
```

前端默认代理到 `http://localhost:8080`；需要改动时使用 `frontend/vite.config.ts` 定义的 `VITE_DEV_PROXY_TARGET` 和 `VITE_DEV_PORT`。首次初始化参考部署说明，在隔离实例中完成。测试和联调优先使用 mock/离线输入；真实付费调用、支付、发邮件和生产操作不作为默认验证步骤。

### 按改动范围验证

下面的命令均从仓库根目录执行。按范围选择，并在 PR 中记录实际结果和未运行原因；这份清单不是已经通过的测试报告。

| 改动范围 | 建议本地检查 |
| --- | --- |
| 所有改动 | `git diff --check`，检查改动范围、凭据与临时文件 |
| 纯文档 / Issue / PR 模板 | 校对内容、相对链接、YAML 字段与必填项；不需要启动服务或运行完整应用测试 |
| 后端 | `make -C backend test-unit`、`make -C backend test-integration`；`(cd backend && golangci-lint run --timeout=30m ./...)` |
| 后端并发/资源生命周期 | 对受影响包增加 `go test -race` 检查，记录 build tags 与测试范围 |
| 前端 | `pnpm --dir frontend install --frozen-lockfile`、`make test-frontend`、`pnpm --dir frontend run build`；为受影响功能补充定向 Vitest |
| 前端完整测试 | 需要全量验证时使用 `pnpm --dir frontend run test:run`；`make test-frontend` 仅包含 lint、类型检查和根 Makefile 中的关键测试集 |
| Ent schema / Wire | `make -C backend generate`，检查生成文件，按 [迁移说明](backend/migrations/README.md) 验证新旧数据和升级路径 |
| 部署脚本 / Compose | 对修改的脚本执行 `bash -n` / `sh -n`，运行 CI 中对应的离线部署测试；不要用部署入口做语法测试 |
| Release 辅助脚本 | 按 CI 安装其 requirements，执行 `python3 -m unittest discover -s .github/release-tools -p 'test_release_matrix.py'` 及对应 shell 语法检查 |

后端定向测试要使用文件对应的 build tags，例如 `(cd backend && go test -tags=unit ./internal/service -run 'Test具体名称' -count=1)`。更换为实际测试名称，确认输出中不是“no tests to run”。Bug 修复尽量验证同一回归用例在修改前失败、修改后通过；无法验证修复前行为时明确说明。

需要验证内嵌前端的二进制构建时，先构建前端，再执行下面的命令。它只在本地生成文件，不部署服务；构建之前检查源码是否有未提交改动，并在 PR 中记录：

```bash
pnpm --dir frontend run build
(
  cd backend
  VERSION="$(./scripts/resolve-version.sh)"
  COMMIT="$(git rev-parse HEAD)"
  CGO_ENABLED=0 go build -tags embed -trimpath \
    -ldflags="-X main.Version=$VERSION -X main.Commit=$COMMIT" \
    -o bin/sub2api ./cmd/server
)
```

UI 改动补充可复核的前后截图、交互步骤与相关语言覆盖；截图上传到 PR 附件，不将 PR 对比图片提交到源码仓库。`docs/screenshots/` 仅保留文字验证说明与历史链接，长期使用的教程配图放在 `docs/images/`。性能改动补充硬件、并发、样本量、测量方法和前后结果。mock 测试、集成测试、CI 和生产验证分别说明，不把其中一种当成另一种。

## 提交与维护 PR

1. 标题概括用户可见的改动，可使用 `fix:`、`feat:`、`docs:` 或 `test:`。按 [PR 模板](.github/PULL_REQUEST_TEMPLATE.md) 填写问题、行为变化、复现/验收、实际验证、兼容性和文档变化；小型文档 PR 可简化不适用的小节。
2. 完整解决某个 Issue 才写 `Fixes #编号`；部分修复、调查或补充文档用 `Refs #编号`。汇总 Issue 中的其他问题仍未解决时，不应自动关闭整个 Issue。
3. 自查后只提交相关文件，推送自己的功能分支，在 GitHub 选择本项目 `production` 作为 base。不要直接向生产分支推送未经审查的改动。
4. 根据审查意见在原 PR head 追加修复提交，保留原作者提交及上下文。不要先合并再补修复来绕开分支权限；没有写权限时保留补丁并说明具体阻碍。
5. 追加提交或解决冲突后重新运行受影响检查，CI 结果应对应当前 head。将失败、跳过和未运行项写清楚；没有真实验证过的结论不要标记为通过。
6. 涉及配置、API 或迁移时说明默认值、升级步骤、旧数据兼容和回滚限制。数据库迁移不能仅靠回退二进制撤销。

使用代码生成或 AI 辅助时，贡献者仍需核对实现、依赖、命令和证据；不要提交虚构的复现、测试结果或不理解的生成代码。对无法验证的部分明确说明。

PR 合并、Release 发布和生产部署是独立步骤。PR 中提供构建或测试结果不代表已经上线；发布与部署由维护者在对应流程中单独执行和验收。
