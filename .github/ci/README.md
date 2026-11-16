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
