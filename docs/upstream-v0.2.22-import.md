# 上游 v0.2.22 完整引入

生产基线：`4b78bceae41c42b9e1241cb0a7515f4b314222d0`。上游 tag：`b0fa9f3e6eeab1968a53e1289b69daa4bb40ca2b`。范围为 v0.2.21..v0.2.22 的 108 个非合并提交，补齐 2 个前置依赖。所有源提交均保留来源记录；重复工具链与已引入 Gemini 格式补丁保留空提交。

完整引入之后追加 fix：恢复 fork 版本/README、保留持久化账单和调度隔离、修正 Ent 描述索引，并登记新增功能提示。版本维持 2.21.6，本次不发版或部署。

| 上游提交 | 引入提交 | 主题 |
| --- | --- | --- |
| `6dd5a045b62e27e5b5b139adef91a171097f639b` | `bf573635b1043796ff6b1bf5ca344c3e4cd6b5c0` | chore: sync VERSION to 0.2.21 [skip ci] |
| `530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484` | `28955fe15e507fddde962df0a9127ebc44098221` | chore: update sponsors |
| `a293214977a5536fed44b50e46a39e37032ec7bd` | `a064edee91539500fe24d4d6cf4bcbc74814df02` | chore(ci): bump Go toolchain to 1.27.4 for GO-2026-6859（空提交，改动已覆盖） |
| `c115dc27ab2e21e121874555b630ba77348badcf` | `636164485a38265523b60837155dcca3c3b0d638` | fix(usage): read Fable weekly quotas from scoped limits |
| `43511b40e977060ba173b68dbb6fd1e89d2fe578` | `51c8f52cb73ac3b3accad777f022dd1c50fa0ca0` | fix(payment): make Alipay refund request id stable across retries |
| `76221c9cb04a8b09484626456f33fcc75b4ce697` | `c8f2515d8a8a086f07ef75e8e0779efa90d28acb` | perf(billing): stop querying the RPM override on every request |
| `37a0f3870b5a269b2957987d86bc76022710bca1` | `67b3ecf49af157bfe27c5e0935f27cfa8790eafc` | fix(billing): scope RPM override to the group it belongs to |
| `1546c0c18e97225cc657444ea8406d1088ebd740` | `754b26712d5d89b7ddb000cf5099c7800ad8b60b` | perf(gateway): skip JSON decode for forwarded Anthropic stream events |
| `98a44ba3f36f3acfd77cebc796fc10248c582018` | `02676fa99a677bee47d2ba2bb113c19c0619bd2b` | fix(billing): make group rate/RPM sync transactional |
| `5846bfbbfaccb3bfad8f3bc006e2619306aaadb4` | `f219b75b71333f6b21d0c6a1240d63df18781ae5` | fix(gemini): 兼容入口 chat 请求转发 response_format 结构化输出（空提交，改动已覆盖） |
| `74ba91aa6b6d130d0b0029fabffed6ce3425dffe` | `1a2a7ec0d4f511b3c740d16aa3bb917265e3943b` | fix(openai): keep Codex forks on their parent's prompt cache key |
| `71ef5770547e16007ea0c41ad94f43698d091d59` | `27fb6fa0628a40dd1e74864fc7b144eabc02d323` | perf(gateway): trim per-request JSON work on the Anthropic request path |
| `a8481f0dec1bd319a1cdefec79363ccf7ee8ce2e` | `85d11ab5db0b92e8e5811ff67a4b90915a999335` | test(gateway): satisfy errcheck in request JSON tests |
| `bd48b2b2d2f037ef56e13ab246d912ce38aa901a` | `38107cffead1abf7655659e5bf64241ef136a0df` | fix(antigravity): keep compat streams alive before first content |
| `e055d7bafc5b7eda444e0ec7c7aed99e2c802de1` | `fa7308502f8d4e46cbd70df4288d56c0ba4f7187` | fix(antigravity): stream Gemini compat thoughts and anchor pre-content timeout after headers |
| `23ac69a0219c77e29de7b8f4195a40ccc745a7b4` | `f90a25dae28d64f257c563305c028b1938840c17` | fix(bedrock): 按错误语义区分封号、用例缺失、限流与内容过滤，流中断保留 usage |
| `fe40612b804dc848be85fcf677bb4e23824918d8` | `5da0a7d99ecac5e5889c89c191c9d4f974abfa89` | fix: allow disabling concurrency wait pings |
| `4584dcb39228ec606193f63d79b7a465e8751c1e` | `e3b25a43b4b3c32c2deaadbde1741862927a7935` | fix(apicompat): retain encrypted function call content |
| `197109ebce6ab4700052aad261b7200574b8932e` | `be758c0b92b6de465542af9092f5e21f373311a1` | fix(auth): 登录回填 email 身份失败时不再 panic |
| `829bf2cdb9a6a235a5d447ccd92405dbbc123577` | `f9160ce55a170d41044d347aa8a51c981634d612` | fix(usage): 分组日汇总重建与保留期清理不阻塞 usage_logs 写入 |
| `fa910c1c3842a627a3b9372bf73e7cee11b26593` | `0457091f2e5dfdbf4ea7946f5c60202f72d79a24` | fix(usage): align five-hour window start with active quota resets |
| `6c79fdfc7ab55e2c067b475ee4a498efd12423c2` | `a36d68212f993436db9bee4f540b7eca037a32e5` | fix(apicompat): send mid-conversation system messages to Responses as developer |
| `c829317fc5987b5d0608c861230b45325dd70d31` | `3b75929d5e3787c0c67867773fc72d9019ac27a5` | fix(anthropic): keep weekly usage readings across 5h rollovers |
| `733722f8c7de5f459a490957c36344cb8e20b019` | `9e9c9ad2432b6213affbfb0fef2632c73b33732b` | fix(gateway): retain sticky writes after client disconnects |
| `d30179f4b5c8f56750fea3d6a7a3f74d50796230` | `5791581edd0e3e60e337394749e14d8427b97c6d` | fix(usage): retain native Anthropic response observations |
| `ac981240cc44f4e61bf76aa1b67eb1a5436aab30` | `17a73f3e7a4cc67fa66c4c5e80da121804f7231f` | fix(usage): retain response models through Chat fallbacks |
| `636981b28b22fb217222035617c77e7cf7adbe60` | `ba5df347ecc4f4cb58f106a2fbfcebc5a46b9b34` | test(scheduler): reproduce cooldown expiry absent from snapshots |
| `39bac35a374f53264a353b69ed21661de9849971` | `956be3029714507c057f5a38438c209bff027cf5` | fix(scheduler): retain cooling accounts in snapshot membership |
| `203de719c3359936579eaff1819ace9b4302000c` | `7e113642bf00e65a7cc4e10a859b2ad23fba8ea8` | perf(openai): avoid repeated request copies in OAuth metadata cleanup |
| `4f8e9ae2f188f856266be7086d370988004a3faf` | `39e3ff0ecce1ae2be9da87764625e4a1a6692626` | test(openai): handle metadata fixture builder return values |
| `97705d147edef9ae673b2c0bd200b9f8b0558b7a` | `cc20309fa29398b8fe2812feea7ff240e6124f1b` | fix(accounts): reject wildcard model whitelist entries |
| `24d77b3306385e2ccb2d3336b3ea1a4aad4cb438` | `cd98310470575b458bd15c345ff7c142e668d45b` | Add English whitelist validation hint |
| `346a7dac0261416e5cb1edc05317f68796417020` | `01690ff7b3b951a8be8f7d7f262e6aee84ad6b26` | Add Chinese whitelist validation hint |
| `7e75ee4b65f56fadce41b093c106c33c6663b12e` | `5866f6ad6f9acdce62a37292b3a2ce9f166d7108` | Cover wildcard rejection and corrected model input |
| `b0f6f8391153ee4ce6753d1733ab54a1c2c81b71` | `348546db95e639ae154a66cf33edc9ddcea9531e` | fix(claude): recover requests after concurrent token rotation |
| `849aa593d4ac56519e8b5fb98fb7031d3cbe0d43` | `85653b47c02520adac10fe4909fa942a6bad74d7` | fix(cn-quota): refresh usage cells with account snapshots |
| `e95e9eb748b9eb2c2abd90abc8f4b7c5d73f3b11` | `82e71135d7b74ebebe4ce0750ac9c5758beceb3c` | test(cn-quota): include snapshot regressions in critical CI |
| `d850b8e3a25ce1e19d9e6c23965344000927a4bd` | `a72827cd5c17f2c6ec26478190703775e0a94ac1` | fix(gateway): enforce effort policies on Anthropic compatibility routes |
| `177243016569651b7dc9141a58b13d97cd5b8a88` | `ccab10f111d5351610215b8cbd9e8738a7976365` | fix(accounts): keep Gemini usage visible during refetch |
| `6d7e6b9d842a9a791398cf1c5f41c19e2a09dba6` | `5017235d222a027355a7cf16622758977ba7586b` | fix(gateway): record native Anthropic endpoints for error diagnostics |
| `bd89d3d943c0b2dafaf927f6665ea90ccfaaada8` | `98912417e0f78a84ad88b7b0bb54587b6ae21092` | fix(gateway): preserve message output config with per-turn beta |
| `fc8af440ed517602b682dc9d2c1630eadf6f4392` | `b7fe8a5c8ee8227d87e010ea12b84e839ee2220d` | test(billing): fix peak-rate usage test clock |
| `b9d02c31a5ba47d21262ad50a37599456033e861` | `0d655b5d88e5a3185b7d7c2bc847c5076264c584` | fix(gateway): observe Anthropic chat completion models |
| `de34f50c1a2a723ae8717f56c964f1ebef3fc6ea` | `71b4679a11dcf9d71deec4cb293cae2e7b2be8f4` | fix(gateway): observe Anthropic Responses models |
| `c487b215faea7a4158c20e74c5796d51c4393471` | `e3b7a7df0948ab5c35de3cfd0ff546548e68fc0a` | test(gateway): cover Anthropic compatibility observations |
| `90f115132490b39be090459455cdb51fdda43df9` | `5539a70d75d953fb9ffa943eee43850abc339425` | test(gateway): add observation regression cases |
| `fd648775810ed4348a44daaacaf38042d4927486` | `ac11822b06f8937fcab90082699264557bf05a25` | fix(codex): preserve additional_tools declaration IDs |
| `8dcdfff22a7ad9568a1c2f88900809dd8eeb4f56` | `f87861c2db77a529849783d2c215eaefc00a3adc` | fix(codex): normalize UUID additional tool declaration IDs |
| `734b922bd8e71b33ff34901c1601d49afa8dbc3d` | `1774f7494f23591204c2df192ab47de539e87e53` | fix(claude): default account tests to claude-sonnet-4-6 |
| `8480cb0cd95710de66052dabf364f6e62d267f0f` | `e8dffcea03c1553da3a88ed1f53e09a71905fd76` | fix(antigravity): match schema items to the selected type |
| `d085bb1a377bb42248da3bfa6d12cf205769fb56` | `0fb41762da55a0766dbcdafc780060825613048d` | fix(apicompat): default json_schema strict=false when converting CC to Responses |
| `c54ebd4f124e88ece35dcc97cff9266ad4d61090` | `1e409ea8fb08fd184d7f08f391fa350196adcb25` | fix(kimi): parse new monthly quota plan and clear absent window snapshots |
| `694d09379b24d93a4a57ad5dda1c0bf8d940707d` | `05256104bdc74a6e721a65278844909a6a8a2a57` | fix(kimi): harden new-plan quota parsing and absent-window cleanup |
| `a0a03be96d81fd1aae4a989500cd4a2752212e80` | `6a5f6241f480d0a327b79fe83d0a0079ab1e0bbb` | fix(openai): normalize additional Codex context on HTTP Responses |
| `f1394a1ab4ab22e2e221e0df20a6d898074567f0` | `00ac5ce83d179b0cf59c83de6bf1b37c8b9dd1ab` | fix(web): stop caching settings-injected HTML |
| `4114fedd1892ee8b14aec40c716f5685edf9663a` | `097d10be75d8ea4c9743f1754abedf46bd79b41e` | fix(ratelimit): lift Anthropic Fable model limit when the 7d_oi window recovers |
| `1f2242436293c1a64e5e17b17ca1033af902f232` | `beeaf6295a690c54121ba8600556e5adda9e9c82` | fix(gateway): keep client metadata.user_id in Chat Completions to Anthropic |
| `2b86d2f6a023bc7d1442bc2a350cb952d3616cb6` | `d15b4425f999250b832ffcc11b822c8133b39173` | fix(gateway): stop duplicating headers on count_tokens passthrough |
| `a0340d4ddeb5831e53910dc7f93c3d3fc95bfb53` | `56b7ce09ec25e1c446fdcb0c530d0c4248171b6b` | fix(websearch): honor account mode on every channel-toggle platform |
| `aee71b85b9df233f914ddd6be46f51a2edd40f23` | `fe069ffc39310a5af201e29e46c50b2536f0a8f1` | fix(gateway): restore public model in composite-routed responses |
| `7349750c017ca0d4efbb63cc109c6692fcc4d228` | `9e09259768991c122dc0112b9d736483bd038f78` | fix(gateway): limit composite model rewrite to message_start events |
| `c45ef4116f0cb680a6fc87bb2e75d78869140e81` | `2e2576a5e1d4b0eae600219375316f74795ea672` | fix(pricing): check remote hash in background at startup |
| `5854a1db43ad36f2c9c42e66f3dd1147a1bccff8` | `576c8107fff9f0c1e16fd14dc6532eae390643da` | fix(upstream): detect dead upstream connections in about 30s |
| `94e56dd192c14b300d9dc514b276164263a5b160` | `6d4e35d77e744135b819e104e089c187e8efe6d5` | fix(upstream): do not fail the dial when TCP_USER_TIMEOUT cannot be set |
| `d05e8ef033db09bef1c77fe579801591322bc7cb` | `9100e700f27ac85eff0f1c24628a09247aaac44a` | fix(usage): 按账户筛选时模型与端点分布的实际扣费不再替换为账号成本 |
| `82cd13d1ec23420563a72a011ec94a98be5e7a63` | `8939e11d0bd1b1e2cb536e6001d0a722f3991ea9` | fix(openai): fall back to memory when first-output spool creation fails |
| `38eb97bd7a2ac4635e0ba5e38a963b49554efa58` | `22da8d6b4a2d89a805ca9497ffe8d23e723efd17` | fix(openai): fail over Messages bridge errors before first content |
| `f1013f177f7a5918af5816f0235a61ea71699efd` | `bbb9a2526f17dc24be77f7c134320d5fb6f6f474` | fix(openai): SSE errors after keepalive, passthrough rules on exhaustion |
| `c1c9d195b3a4c7f4af5673d032c12083d4ae8cb1` | `5eef32a3a5eed62716c9a70e4131ef381d49e291` | fix(openai): forward upstream keepalive events before first output |
| `975b2807d12966823ace2428290ce53c72d59dc6` | `5f99b5359f38a4048af6bc602db7639d1b2fcfe8` | fix(openai): apply account side effects to stream errors after output on compat paths |
| `b7878328b699bd5f5b61e10df2db74a3723dfc7a` | `3e8c44eba014af02ff09dae002d9b9fbf4233fdc` | fix(pricing): honor explicit DeepSeek token price overrides |
| `d3716a1730e81ba53fd9011ae6091b670a72f6df` | `a7903d5ddfd9604ca78e5442c2137dd592c32e10` | test(antigravity): a committed empty compat stream should release the session's sticky account |
| `5cbcb1b59104333a74520e4da6d57de8180358da` | `354689c0c688fa1d7636c83e30ccc6ff7290461f` | fix(antigravity): release the sticky account after a committed empty compat stream |
| `b0d60a8c867b54919bccdb6dca8eb86dde2644b5` | `a7296c8deed834d79b967e7aa8ecbe42e4368f7e` | fix(gemini): separate same-name parallel tool calls in streams |
| `27a61be4c9e72857fcde5b0d6085d31bdb73d10b` | `d3024b4d302702b6aed7fe7d198a6ac9b5125450` | fix(gemini): preserve final snapshots of fragmented tool calls |
| `ddb7eac6330af796c92e940f0112d34c36a24811` | `08d4300e2a836421148e21821d68a0017432e6c7` | chore(go): upgrade to 1.27.4 to resolve security check（空提交，改动已覆盖） |
| `ce026a2cbf1e1d366ddd64fe098e8a0334949a7b` | `11276d072ffe97aa014721b02972c5d2985dfd64` | fix(gemini): retain complete string argument snapshots |
| `cca745e8beefa08e1afa1312e64f7f04b98bcfce` | `48e1ecf4a1653ffadde68ae7abaf2f51f9f9144f` | fix(scheduler): keep openai_capabilities in the scheduler projection |
| `5c96ee4d2941b484fe1960a8d57acaa739b62507` | `ff01bdbc6ee47f074069a0eab2d4b6b64515b39c` | fix(channels): persist Bedrock compatibility as a boolean |
| `32e70f3d4048ce5978dc15f72d5dd81f2e383a28` | `1fefd4c383ac17d76f4a0a15ae4a0eb8565171c5` | feat(gemini): 补齐 Gemini 4 Hydrogen 计费 |
| `b32ad6dfe113ed3e0ad64679d5c06adec6241961` | `c4c0d0c786fb0e6c5ec3d4be7b67278b13549a0e` | feat(gemini): Vertex 账号支持 us/eu 多区域端点并按模型降级 global |
| `68071ddde1ee4de8fb31b82536568cd831fde7be` | `2f6f7c35e87e11cbf4fec58bc40aa3443823f792` | feat(gemini): Vertex 多区域端点按实际请求区域加价 10% |
| `2164ea48781731d3d129268d33c4220f55e6183b` | `ba058ec352d42cac35655b89f886187d2a63600c` | fix(billing): apply Vertex multi-region multiplier to default pricing only |
| `b6c21cd5e75acfab663a5b916572d9ed56c70d8a` | `2c8da7d5c00434dd8a39b6ee92b1eb32aa8b5792` | fix(gemini): 原生请求按稳定前缀生成粘性会话哈希，换号按上游实际用量计费 |
| `8d57f461ca85f89cef0bf94ae71acc01e35042ca` | `8e9d3bd8c80153380bad090cc4d8090609d294d1` | fix(gemini): keep forced cache billing on sticky-session failover |
| `2101f4f94005518529f0b391d823ef1cd2ade4c5` | `ab322ae2baf7404bc1077d95024834e8dbc9e232` | perf(billing): 同一用户的扣费按用户通道合并结算，消除余额热点行排队 |
| `ade20f4e4749b743dab268e006d3ad10b58af554` | `e0d1a97970792ec1b93254f1ecce9abde5de2035` | perf(gateway): 请求体热点路径改用零拷贝读取，减少整段解码与复制 |
| `b46b3cbabcca6781a013a38140f151786334e930` | `409c7395d7ede841014436ba9e40c2e722bee39e` | test(handler): follow normalizeCodexUntrustedContext rename from #8769 |
| `9c3280098623989bac4d631fd8534d1c0e0ac672` | `856c990a3362e1692b055f1e1c5bbea06588d590` | perf(scheduler): 调度快照加载与数据库回源按 bucket 合并，减少大分组选号的整组复制 |
| `d70a23f84b7329ca76dacae590df132993d415fc` | `029b5e3b8ff2de2d80df73adbb03f6f55a64dcea` | feat(gemini): add Gemini 4 Carbon support |
| `70ad8365d9b5a0be5ebaf3c0065ff385f8c081fd` | `b4647020bb3cfd85d6aef8aaa29412bd0480cb33` | fix(grok): keep xAI server-side search calls out of tool calls |
| `9d5fb150b9fd8b0febed777da618b17cdf9f876c` | `9d5f07a57d86073ac5805e3e26118044dfa96ecf` | feat(openai): optionally filter passthrough accounts by model |
| `121a4cca17ae153bf675f40d14919738c0411f70` | `4388d0c18a8b58d9a44ebe63688427a380408bea` | fix(scheduler): keep openai_passthrough_model_filter in the scheduler snapshot |
| `1280006a1f3bb7e8b29b487410b5763e06594cd1` | `77635e8c1874c0e867682c4548ad4056f5d2ede2` | fix(gateway): emit SSE errors after queue keepalives |
| `dd6329b983eb3b3adf61159944d3c3644d14048f` | `52493b1f3c7f4b547ceb6db80e7751d9da9574ea` | fix(openai): 清洗 Python re 不支持的工具 Schema 正则 |
| `e0cf4984f0d7c0d54ccb2d36292272a4231c07be` | `53fd82cf8364d5e65b19203e564c322cd7159266` | fix(openai): 删除 patternProperties 时放宽封闭对象 |
| `af187f9a50fcae3884875e94338088990dec00f8` | `f324430034b4a9fb5cdf6fc29282dce224ae66b5` | fix(openai): 保留 patternProperties 删除后的 additionalProperties schema 兼容性 |
| `6613ced57314baf6f70fe589d295b3982d6d36a0` | `87f2ff244fb8fb26bdd9339b1fa3097b6d83f9cc` | fix(openai): let enclosing tool-schema edits supersede nested ones |
| `c8f4264f8fe8a450e1448f5d4ba07d768243ba77` | `74d2e05c1697c325e94d3a049953d0a531a5f574` | fix(grok): preserve upstream media error messages and codes |
| `03a489e43e5103a488387e6b96602bff57a73af7` | `7ca6dbb027f813063b83a66ca593376eab95ab34` | fix(grok): keep xAI flat error parsing out of the shared extractors |
| `e677462549985a360fe032d2b66334bbbdfcad12` | `e652c5920dc2f8af13b523175add7b192da62f45` | fix(openai): preserve quota freshness when refresh fails |
| `c4ff9b90a917ff8b321a92819d143c969dbb0d82` | `bf90ffa7803a13e3e1ba5ba306eea9bda60b70c9` | test(openai): check quota snapshot timestamp type |
| `bf3951a59e23974f6efecfe62e835c701e73fa25` | `572b705c48098f8fa86a02546e4ca3a1543c625b` | fix(monitor): don't flip channel monitor to error on OpenAI quota refresh failure |
| `9cd1a1eb21e84707685e1998dcb5a3d1ceafae49` | `147429c717efa2605e9cbb35a3a49292c6bdb02d` | chore: update sponsors |
| `330198321f2f1862397363cae6d451dbf9a6ff76` | `db5a7b2b7d2b617ce0cd205f82cad319fadbc1b2` | feat(groups): 分组支持强制 OpenAI Ultra Fast 档位 |
| `e257b78117d7668078fb65a38a94389b95a181e1` | `b5b647cb833389b4b28800b58284e468b434ed41` | chore(ci): bump Go toolchain to 1.27.4 for GO-2026-6859（空提交，改动已覆盖） |
| `ad9eeedebbd87fb2e0d311fed7c5e7369b15d02e` | `6b52fab927448e8c9718ba8d8a2f4c74851c233b` | feat(groups): 强制 Ultra Fast 按模型门控，可与强制 Fast 并存 |
| `926d3cd86d8b104d59c1dfd474863d9a0223c953` | `3838cfe47892dce901b510a529fa5b627e975266` | test(billing): 将 Ultrafast 门控与计费一致性用例移入 unit 标签文件 |
| `43c1ca01483c4e21b2dd62bd1168cf374436b9ca` | `6e280d03f36883d0a34e23f32aa89796e8361158` | fix(billing): 为硅基流动 Qwen3-Embedding 系列补兜底定价 |
| `796455c1c9f57241c82115e78dcb67938609af4d` | `5e4e39cea6156e4bc736e1a2d117bccb71357e9f` | fix(billing): 为硅基流动 Qwen3-Reranker 系列补兜底定价 |

## 合并提交的独有变更复核

`git show --remerge-diff` 检出 `7f3fe5bfde146f940aaad88e89f6e8944e1f5ceb` 含调度配置池与合并加载的额外整合：缓存读取按当前时间筛选、回源发布完整池及两项回归。PR 审查追加 fix 补齐该差异，保留 fork 深拷贝与撤销检查；不重复重放整个 merge。其余范围内 merge 的 remerge diff 为空。

## 后续验收适配

- Fork 对客户端实际使用的 UltraFast 维持既有 6 倍计费；上游新增的分组强制开关仍只作用于支持的模型。测试同时验证这两个独立约束。
- 供应商明确返回的 `service_tier` 独立于 `model` 保存；Grok 的扁平拒绝消息按其实际字符串解析，仍保持请求级拒绝与账号可用状态。
- 调度测试使用新的配置池查询接口，Redis 隔离前缀覆盖多键 `EXISTS` 的所有键；故障注入与状态断言保留。持久化账单包装层继续透传 journal 与失败缓存延期接口。
- 日汇总清理使用仓库实例的配置时区，不读取全局时区；保留期及夏令时边界由对应 SQL 断言验证。
- 历史 RED 在独立目录保持选定缺陷源码与业务断言，只适配不属于目标验收的新增测试依赖。当前候选 GREEN 保留完整测试；编译失败、跳过和基础设施错误不能当成预期 RED。
