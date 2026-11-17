# 按改动范围运行 CI

主 CI、安全扫描、VIP 入账和多机验收先运行 `changes`。它比较 PR 的 merge base 与 head（production push 比较 before/head），仅启动相关 job。后端改动仍执行完整后端单元、集成和 lint；前端改动仍执行完整前端关键测试、类型检查和 lint。主前端固定 GitHub runner，Node 堆上限 6 GiB。

范围在 `scopes.json` 中维护，包含源码、依赖锁文件及检查使用的辅助脚本。Markdown/RST 文档不计入应用源码指纹。job 命令、runner、环境变量、权限或依赖配置变化仍会触发对应检查；仅修改触发规则、归档工作流和本选择器时由 `CI selection rules` 验证。修改某个检查的依赖范围时应同时补充范围用例，不能为了转绿漏掉依赖。

对于同一 PR 的后续提交，选择器查询原 PR 最近 30 次对应工作流，只有相关 Git 文件对象和 job 定义完全一致、且对应 job 确实成功时才复用。production 合并则检查实际父提交的已成功 job。失败、取消、跳过、未完成以及其他 PR/fork 的记录不能复用。未显式 checkout PR head 的旧任务可能测试了另一棵合并树，也不复用；当前两项多机验收采用这一保守限制，只按范围跳过。每次决策在 job summary 记录原因和旧 run/SHA；复用表示旧证据适用于相同输入，不声称新 head 已重新测试。查询失败或取不到旧提交时，受影响检查正常运行。

手动 `workflow_dispatch`、`v*` tag 以及安全扫描的每周 schedule 强制执行对应工作流的全部检查，不复用旧结果。安全数据库随时间变化，因此每周安全扫描始终保留。选择器仅使用 `contents: read`、`actions: read`，不创建虚假的成功 check。

专用回归保留功能路径；共享提示 ID、类型登记和通用翻译由主前端检查覆盖，避免一处文案变更触发多个功能的后端/截图验收。历史 feature 分支 push 入口移除，有 PR 时自动按路径运行；需要独立验收可手动触发。

验证选择器只需：

```bash
python3 -m pip install PyYAML==6.0.3
python3 -m unittest discover -s .github/ci -p 'test_*.py' -v
```

它使用临时 Git 仓库和模拟 GitHub 返回值，覆盖删除、重命名、依赖变更、检查命令变更、旧成功复用、失败/API 异常回退及手动全量，不运行 Go/前端应用测试。

## Go 测试并行与耗时报告

后端选择器分别决定 `unit-test` 和 `integration-test`。单元测试在四个 GitHub 托管 runner 上按 package 分片，集成测试在独立 runner 同时执行；`test` 保留为汇总检查，任何应运行的分片失败、取消或跳过都会失败。复用单元测试证据要求四个分片全部成功，且源码、辅助脚本、分片权重和 job 定义一致；手动/tag 仍强制全量。整个旧 workflow 被新 head 取消时，汇总 job 也取消，避免 always() 任务在队列中阻塞后续运行；取消状态不算通过。

`.github/ci/go_tests.py` 从 `go list -tags=unit ./...` 动态发现全部包，再按 `go-test-weights.json` 的历史耗时从重到轻分配。新包按 1 秒估算并自动纳入；无测试包也保留。权重仅影响分配，不筛选测试。初始权重来自 [2026-11-17 的成功运行](https://github.com/ranxi2001/sub2api/actions/runs/44150112506)，其中 handler 581.520 秒、service 363.216 秒。包内用例尚未进一步拆分，因此单个重包仍可能决定单元测试关键路径。

运行仍经过 backend Makefile：`test-unit-packages` 接收包列表，嵌套收款模块的 `test-collection` 在第 0 片执行一次；默认 `make test-unit` 仍包含两者。单元测试保留 Go 测试结果缓存。集成测试保留 PostgreSQL/Redis、Docker testcontainers 和 `GOFLAGS=-p=1`，增加 `-count=1` 确保外部服务检查实际执行。原先单独运行的 `TestUsageTrendAggregateTimezone` 已包含在完整集成套件内，不再重复启动仓库集成环境。每个单元分片和集成 runner 都安装并检查 redis-server，分别满足快照适配器和认证缓存跨进程 Pub/Sub 用例；服务容器不能替代这些用例自行启动的独立 Redis 进程。

缓存按 OS、架构、go.mod/go.sum、suite/分片与源码 SHA 分隔，按同一依赖和分片前缀恢复最近的编译缓存；各分片不竞争同一写入 key。相同 key 的成功重跑可直接命中。分片缓存未命中时，使用与 setup-go 相同的绝对路径和键格式，按当前 Go 版本/runner 镜像回退读取 production 已有的共享缓存（先匹配 go.sum，再匹配版本前缀），只向分片 key 写入；避免新 PR 因无法读取其他 PR 的专属缓存而完全冷启动。独立 runner 会增加总资源和冷启动开销；这里不变更 Runner 账户配额，也不使用生产服务器编译。

每个 job 输出 summary 和保留 14 天的 `go-unit-<shard>-<attempt>` / `go-integration-<attempt>` artifact：

- `*.jsonl`：原始 `go test -json` 事件流，可直接使用 jq；make 命令回显等非 JSON 文本保留在 `.log` 中。
- `*.log`：还原的测试输出；编译器 stderr 另见 Actions 日志。
- `timings.json`、`summary.md`：命令墙钟时间、退出码、package / case 最慢 30 项、pass/fail/skip 计数。
- `shards.json`：四片完整包列表，便于核对漏测或重新平衡。

[Go JSON 事件](https://pkg.go.dev/cmd/test2json)中的 Elapsed 不包含编译和 Runner 排队；命中测试缓存时可能回放之前的耗时，嵌套/并行用例也不能直接累加。评估加速应同时比较 Actions 的排队时间、job/step 墙钟时间及新报告，不能把四分片等同于四倍加速。首阶段保留完整回归，不引入基于依赖图的用例裁剪；慢测试时钟改造、进一步集成分片、Runner 扩容与发布缓存按采集结果另行评估。
