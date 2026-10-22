# 配置 BEpusdt 加密货币支付

Sub2API 已将 [BEpusdt](https://github.com/v03413/BEpusdt) 的收款引擎编译进服务器，支持余额充值和订阅购买。新建实例默认使用「内置收款」：钱包、节点、汇率由本站后台管理，链上扫描在本站进程中运行，付款页面也由本站提供，**无需单独部署 BEpusdt，也无需填写网关 API 地址或 Token**。

已有外接实例保留原配置和行为，可以继续使用独立 BEpusdt 网关。

## 在服务器部署内置收款

1. 更新本站前后端或 Docker 镜像。确保 Sub2API 的 `DATA_DIR` 对运行用户可写，并持久化该目录；标准 Docker 部署使用 `/app/data` 数据卷。
2. 进入「后台 → 设置 → 支付设置」，开启支付，选中「BEpusdt 加密货币」并保存。
3. 展开「内置加密货币收款：钱包、节点与汇率」，为每个准备开放的币种／网络添加收款地址并启用。只填写公开收款地址，不需要钱包私钥或助记词；同一地址用于多个币种时，分别添加对应网络的钱包记录。
4. 检查节点设置。Tron 填写 gRPC `主机:端口`，例如 `grpc.trongrid.io:50051`；其余 RPC 和 TON 全局配置填写 HTTP(S) 地址。默认公共节点可用于开始配置，实际收款应选用稳定、额度足够的节点。TronGrid 与 CoinGecko API Key 可选，编辑时留空保留已保存值。
5. 点击同步汇率，核对每个准备收取的币种和计价法币有可用汇率。可手动填写「1 个币对应多少法币」；手动值用于新订单，并会在下一次自动同步时被更新。已有订单保留原汇率与金额。
6. 创建「BEpusdt 加密货币」服务商实例，选择「内置收款」，填写名称与计价法币（CNY、USD、EUR、GBP、JPY），勾选已配置钱包的币种／网络。新建实例默认只选中 USDT (TRC20)。
7. 通知与返回地址的域名填写**本站公网地址**，例如 `http://148.113.211.94:8080`，然后启用并保存实例。内置模式仍通过已签名的本站通知接口交付订单，并支持主动查单补偿。

| 用途 | 本站路径 |
| --- | --- |
| 异步支付通知 | `/api/v1/payment/webhook/bepusdt` |
| 本站付款页面 | `/payment/crypto/<本站订单号>` |
| 支付后返回 | `/payment/result` |

在「链上扫描状态」中点击刷新，可查看最近区块、扫描成功率和时间。有待收款订单时才扫描对应网络；首次配置钱包后没有区块记录是正常现象。EVM 转账需达到上游定义的网络确认深度并通过交易回执检查，才视为支付成功。

实例沿用现有单笔、每日限额。存在待处理订单时，不能切换内置／外接模式、更换计价法币或移除订单使用的支付网络；钱包有待支付或待确认订单时，不能换地址、换网络、停用或删除。

## 支持的币种与网络

网络代码对应上游的 [交易类型文档](https://github.com/v03413/BEpusdt/blob/main/docs/trade-type.md)。未勾选的网络不会出现在用户付款方式中；开放前应逐一核对钱包、合约与节点可用性。

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

用户选择币种和网络后创建订单，在本站付款页面查看收款地址、**精确转账金额**、二维码和倒计时。扫码内容是接收地址，转账金额需按页面填写。不同并发订单可能用同一地址和略有差别的金额区分，必须按指定币种、网络和金额付款。

例如，本站订单为 28.88 CNY，引擎换算后可能要求 4.25 USDT。通知中的 `amount` 为 28.88，`actual_amount` 为加密货币数额。本站核对法币金额，按原有充值倍率或订阅套餐交付。链上检测到转账后，付款页隐藏二维码和转账提示并显示「等待确认」，避免再次转账；付款页仅向订单所属登录用户开放。

CNY 通道的订阅购买沿用本站配置的订阅 USD/CNY 汇率；收款引擎再将法币换算成链上币种。本站订单超时时间会传给引擎，低于 2 分钟时按 2 分钟创建收款订单。

成功通知经过签名、订单号、交易号、网络和精确金额校验，重复通知／查单沿用幂等交付。取消待支付订单时也取消引擎订单；进入链上确认后不能取消。本站订单超时只标记过期，不取消引擎的历史补扫，避免节点故障期间已付款订单无法恢复。内置订单的逾期入账宽限期与引擎的 3 小时补扫窗口对齐，过期付款页也会主动查单补偿。在线退款尚不支持。

## 数据持久化、备份与多副本

内置引擎数据保存在 `${DATA_DIR}/bepusdt/`，包含 SQLite 数据库、WAL 文件、日志和持久化的内部回调令牌。令牌由本站使用安全随机数自动生成，不作为后台可填写字段，也不会返回到浏览器。

应在停止 Sub2API 后备份**整个 `bepusdt` 目录**，并与本站主数据库一同保存、恢复。仅备份 PostgreSQL 不包含链上订单、收款地址和内部令牌；丢失引擎库会影响查单和旧订单交付。不要在有未完成收款订单时删除或重新初始化目录。重启时已有引擎库会自动恢复扫描。

每个内置引擎目录由一个 Sub2API 进程独占，第二个进程会报数据已被使用。多副本部署应使用一个集中部署的外接 BEpusdt 网关；不应让多个独立内置数据库共用收款钱包，以免重复匹配转账。

内置模式需要出站访问所选网络的 RPC、CoinGecko 汇率源，以及配置的本站通知地址。历史补扫直接使用各链原生 RPC 查询区块边界，不再依赖 `api.bepusdt.online`。扫描失败保留待扫描范围并重试；公共备用节点经过网络身份校验，自定义节点的凭据不会转发到公共节点。点击「检测已保存节点」可验证区块与交易接口；节点不可用时停止创建新收款订单，付款页会显示扫描延迟。

## 继续使用外接网关

在实例中选择「外接网关」，填写独立部署的 BEpusdt 服务根地址，例如 `https://pay.example.com`，不要填完整的创建交易接口路径。Token 从 BEpusdt「系统管理 → 基本设置 → API 设置 → 对接令牌」获取；列表读取隐藏 Token，编辑留空保留原值。

在独立 BEpusdt 后台配置钱包、节点和汇率，本站勾选相应网络并填写本站通知／返回域名。外接模式打开独立网关的收银台。没有模式字段的旧实例按外接模式处理，升级不会自动迁移旧网关订单或钱包。

## 验证与排查

自动回归还覆盖 RPC 故障切换、失败区块重试、非 EVM 回溯、并发订单状态转换和回调重试，保留外接网关漏回调后主动查单与订阅幂等交付测试。自动回归覆盖真实引擎库的金额分配、钱包保护、配置重启保存、订单归属、确认后幂等充值，以及本地模拟 Base RPC 的区块扫描、USDC 转账匹配、40 区块确认和成功回调。**尚未进行真实资金与所有网络节点联调**；上线时应先用准备开放的网络进行一笔小额付款，确认充值／订阅到账，再重复查单验证不会重复交付。

```bash
cd backend
make test-collection
go test -tags unit ./internal/bepusdt ./internal/payment/... ./internal/service ./internal/handler ./internal/handler/admin -run BEpusdt
go test -tags unit ./internal/payment/...
```

```bash
cd frontend
pnpm exec vitest run src/components/payment/__tests__ src/views/user/__tests__/CryptoPaymentView.spec.ts
pnpm run typecheck
```

- **没有可用钱包**：检查对应币种／网络的钱包是否启用、地址是否正确、汇率是否存在，以及同地址未完成订单占用的金额。
- **已转账但仍待支付**：核对币种、网络、精确金额和交易时间，刷新扫描状态，查看 `${DATA_DIR}/bepusdt/logs/`。确认 RPC／TronGrid 额度和出站连接正常。
- **等待确认**：检查链上确认深度与 RPC 返回的交易回执；不要再次转账。
- **引擎显示成功但本站未到账**：检查通知地址和反向代理，本站回调应返回 HTTP 200；使用付款页主动查单补偿，并查看本站订单日志。
- **金额校验失败**：法币应付金额与引擎订单必须一致，不能把链上币数作为本站法币订单金额。

## 源码与发布

内置版本固定链接 BEpusdt `v1.24.3-0.20260930070044-aa3bd5097f25`。BEpusdt 使用 GPL-3.0，组合服务器的分发需遵守 GPL-3.0，保留声明并提供完整对应源码及构建说明；原 Sub2API 源码的 LGPL-3.0 与此兼容。参见 [第三方声明](../THIRD_PARTY_NOTICES.md) 与 [BEpusdt 许可证](../LICENSES/BEpusdt-GPL-3.0.txt)。Docker 镜像包含这两份文件。

源码构建按仓库既有流程先构建前端，再在 `backend` 执行 `go build -tags embed ./cmd/server`。保留 `backend/third_party/bepusdt` 中包含补扫和节点故障恢复修复的源码，以及 `backend/third_party/glebarez-go-sqlite` 适配模块，它复用本站 SQLite 驱动，避免两套驱动重复注册。引擎入口为 `backend/internal/bepusdt/engine.go`，内置支付适配器为 `backend/internal/payment/provider/bepusdt_embedded.go`，后台钱包面板为 `frontend/src/components/payment/BEpusdtManager.vue`。
