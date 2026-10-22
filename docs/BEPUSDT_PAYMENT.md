# 配置 BEpusdt 加密货币支付

本站通过 [BEpusdt 原生 JSON API](https://github.com/v03413/BEpusdt/blob/main/docs/api/api.md) 接入支付，支持余额充值和订阅购买。需要先部署 BEpusdt，并在其后台配置各网络的收款钱包、链上扫描与汇率，再开放本站支付入口。

## 在后台开启支付

1. 进入本站「后台 → 设置 → 支付设置」，开启支付功能，在支付类型中选中「BEpusdt 加密货币」，保存设置。
2. 创建服务商实例，选择「BEpusdt 加密货币」，填写实例名称。
3. 填写 BEpusdt 服务根地址，例如 `https://pay.example.com`。不要填写完整的 `/api/v1/order/create-transaction` 接口地址。
4. 在 BEpusdt「系统管理 → 基本设置 → API 设置 → 对接令牌」获取 API Token，填写到本站实例。令牌仅供服务端调用使用，后台列表读取时隐藏；编辑时留空保留原令牌。
5. 选择订单计价法币：CNY、USD、EUR、GBP 或 JPY。这里选择的是订单金额的单位；实际收取的加密货币由所选支付网络决定。
6. 勾选已在 BEpusdt 配好收款钱包的币种／网络。创建实例默认仅选中 USDT (TRC20)，可增加或取消网络。
7. 核对通知与返回地址的域名，启用实例并保存。后台会生成以下路径，域名应填写公网可访问的网站地址。

| 用途 | 本站路径 |
| --- | --- |
| 异步支付通知 | `/api/v1/payment/webhook/bepusdt` |
| 支付后返回 | `/payment/result` |

服务商实例沿用现有单笔、每日限额配置。存在待处理订单时，不能更换 API Token、网关地址、计价法币，或移除这些订单使用的支付网络。

## 支持的币种与网络

网络代码对应上游的 [交易类型文档](https://github.com/v03413/BEpusdt/blob/main/docs/trade-type.md)。未勾选的网络不会出现在用户付款方式中。

| 网络 | USDT | USDC | 其他币种 |
| --- | --- | --- | --- |
| Tron | `usdt.trc20` | `usdc.trc20` | `tron.trx` |
| Ethereum | `usdt.erc20` | `usdc.erc20` | `ethereum.eth` |
| BSC | `usdt.bep20` | `usdc.bep20` | `bsc.bnb` |
| Polygon | `usdt.polygon` | `usdc.polygon` | — |
| Aptos | `usdt.aptos` | `usdc.aptos` | — |
| Solana | `usdt.solana` | `usdc.solana` | — |
| X-Layer | `usdt.xlayer` | `usdc.xlayer` | — |
| Arbitrum One | `usdt.arbitrum` | `usdc.arbitrum` | — |
| Base | — | `usdc.base` | — |
| Plasma | `usdt.plasma` | — | — |
| TON | `usdt.ton` | — | `ton.gram` |

## 付款与金额

用户在本站选择币种和网络后创建订单，打开对应的 BEpusdt 收银台。收银台提供接收地址、准确的转账金额和二维码；用户应按该页面的金额、币种和网络付款。本站使用固定金额交易接口，不使用收到任意金额即完成订单的地址独占模式。

例如，本站订单为 28.88 CNY，上游换算后可能要求转入 4.25 USDT。通知中的 `amount` 为 28.88，`actual_amount` 为加密货币数额。本站核对法币金额 `amount`，按既有余额充值倍率或订阅套餐完成交付，不把 `actual_amount` 当作本站充值金额。

CNY 通道的订阅购买沿用本站配置的订阅 USD/CNY 汇率；BEpusdt 将订单法币进一步换算成链上币种。BEpusdt 自身的汇率规则在其后台设置。本站订单超时时间会传给上游，低于 2 分钟的设置会按 2 分钟创建 BEpusdt 订单。

支付完成回调经过签名、订单号、交易号和精确金额校验后入账。重复通知沿用现有幂等交付机制。本站也可主动查单补偿遗漏的回调，并在取消待支付订单时调用上游取消接口。

## 验证接入

部署本站前后端更新后，用已配置钱包的网络创建一笔小额充值：

1. 确认本站只显示启用的支付网络，计价单位符合实例设置。
2. 打开 BEpusdt 收银台，确认网络、地址、应付金额和超时时间。
3. 完成付款后，确认本站订单为已完成且余额或订阅正确到账。
4. 在 BEpusdt 重发该订单通知，确认不会重复充值或延长订阅。
5. 创建另一个待支付订单并在本站取消，确认上游订单也取消。

开发回归命令，在项目对应目录执行：

```bash
cd backend
go test -tags unit ./internal/payment/...
go test -tags unit ./internal/service ./internal/handler -run BEpusdt -count=1
```

```bash
cd frontend
pnpm exec vitest run src/components/payment/__tests__/bepusdtFlow.spec.ts src/components/payment/__tests__/PaymentProviderDialog.spec.ts
pnpm run build
```

## 常见问题

- **下单提示没有可用钱包**：核对 BEpusdt 中所选网络的收款钱包是否启用、扫描服务是否正常，以及钱包金额占用情况。
- **付款后订单仍待支付**：检查通知域名、反向代理和 BEpusdt 通知日志；本站回调应返回 HTTP 200 与 `success`。同时检查两端 API Token 是否一致。
- **金额校验失败**：本站要求通知法币金额与订单应付金额一致；检查 BEpusdt 是否收到本站创建的固定金额订单，避免在上游修改其计价法币或法币金额。
- **退款**：BEpusdt 原生接口没有本次接入可用的退款接口，后台不开放在线退款开关。

实现位置：后端适配器为 `backend/internal/payment/provider/bepusdt.go`，网络列表为 `backend/internal/payment/bepusdt.go`，后台字段和前端网络选项为 `frontend/src/components/payment/providerConfig.ts`。无需额外数据库迁移，复用现有支付实例、订单和配置存储。


## 本次移植的验证范围

2026-10-22 从当前仓库移植到 `BingBu1/sub2api`，基线为 `5f779474d`（v2.16.0）。原生 API 适配器和网络列表保持与来源实现一致；支付注册、订单快照、超时和前端入口按本 fork 的现有接口接入，保留其充值赠送、订阅续费和退款状态逻辑。

| 验证证据 | 覆盖结论 | 对应文件 |
| --- | --- | --- |
| 原生适配器测试 | 全部 23 个网络下单、独立构造签名、金额与身份校验、查单和取消、拒绝跳转请求 | `backend/internal/payment/provider/bepusdt_test.go` |
| 服务层 BEpusdt 测试 | 配置脱敏、待支付订单保护、精确法币金额、漏通知查单补偿、取消交易、充值和订阅重复通知只交付一次 | `backend/internal/service/payment_bepusdt_test.go` |
| Webhook 测试 | 原生 JSON 商户订单号提取与 `success` 应答 | `backend/internal/handler/payment_bepusdt_test.go` |
| 前端 41 项测试 | 各网络下单与页面恢复、默认 TRC20、配置保存和回调地址生成 | `frontend/src/components/payment/__tests__/bepusdtFlow.spec.ts`、`PaymentProviderDialog.spec.ts` |
| 后端编译与支付回归 | `go build ./cmd/server`、支付模块全量单元测试和服务/回调支付相关测试通过 | `backend/internal/payment/`、`backend/internal/service/`、`backend/internal/handler/` |
| 前端生产构建 | 中英文键完整性、TypeScript 检查和 Vite 打包 | `frontend/package.json` 中的 `build` 命令 |

扩大到支付组件、管理订单页面和语言键测试时，136 项通过，`SubscriptionPlanCard.spec.ts` 的 3 个文案断言失败；在未修改基线 `5f779474d` 上单独运行同一文件也复现这 3 个失败，属于基线已有问题。

以上使用本地模拟 BEpusdt 服务，不包含真实钱包链上转账。上线后仍应按前面的“小额充值”步骤核对实际钱包、网络、通知域名和最终到账；配置令牌与钱包不随代码移植。
