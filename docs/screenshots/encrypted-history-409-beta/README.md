# 409 省略加密内容（Beta）截图

来源提交：b392e98f6cbfab0142c4786ea86c0ae87fcd2c3b。
CI：https://github.com/ranxi2001/sub2api/actions/runs/43271213325。
命令：`node .github/scripts/encrypted-history-409-preview.mjs "$GITHUB_WORKSPACE" "$RUNNER_TEMP/omit-history-ui"`。

入口：系统设置 → 网关设置 → 网关转发行为。
这里渲染实际 EncryptedHistory409Settings.vue，使用虚构管理员和组件状态；不是整页导航、真实后台保存或生产验收。

- default-off.png：默认关闭，中文。
- enabled.png：点击开关后的开启状态。
- mobile-dark.png：430px 深色开启状态。
- english.png：英文默认关闭。

没有新增功能之前的组件，故提供新增入口的关闭/开启状态；所有截图均在 CSS 过渡结束后捕获。
