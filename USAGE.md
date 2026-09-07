# 1688 MCP 服务使用说明

面向 Agent/客户端开发者的接入文档。服务基于 MCP（Model Context Protocol）Streamable HTTP 传输，通过浏览器自动化抓取 1688 搜索结果。

- 端点：`POST http://localhost:18080/mcp`（JSON-RPC 2.0）
- 支持的协议版本：`2024-11-05` / `2025-03-26` / `2025-06-18`（握手时回显客户端请求的版本）
- 可用工具：`check_login_status`、`search_goods`、`get_goods_detail`

## 1. 启动服务

```bash
# 推荐：使用编译好的 exe（避免每次触发防火墙）
.\mcp-1688.exe

# 或：开发模式（每次会重新编译，会触发防火墙提醒）
go run .

# 无头模式（登录完成后可用）
set HEADLESS=1
.\mcp-1688.exe

# 自定义端口
set PORT=19090
.\mcp-1688.exe
```

**推荐使用编译好的 exe**：首次运行会触发一次防火墙提醒，之后就不会再弹了。如果用 `go run .`，每次都会重新编译，每次都会触发防火墙提醒。

启动后日志输出 `1688 MCP Server 启动在端口 18080（headless=false，登录态目录 ./user_data）`。

**首次使用必须登录一次**：有头模式启动时会自动弹出 Chrome 并打开 1688 首页，在该窗口手动登录；登录态持久化在 `./user_data` 目录，之后重启无需再登录。

## 2. 客户端接入

### GUI 客户端（Trae / Cursor / Claude 等）配置

```json
{
  "mcpServers": {
    "1688": {
      "url": "http://localhost:18080/mcp",
      "transport": "streamable_http"
    }
  }
}
```

### 代码接入

`examples/` 目录提供三种已实测的客户端示例：

| 文件 | 方式 | 依赖 |
|---|---|---|
| `examples/client_python_raw.py` | urllib 直发 JSON-RPC | 零依赖 |
| `examples/client_python.py` | 官方 `mcp` SDK | `pip install mcp` |
| `examples/client_node.mjs` | 官方 TS SDK | `npm i @modelcontextprotocol/sdk` |

## 3. 工具参考：check_login_status

检测 1688 登录状态。**所有操作前必须先调用本工具**。

- 入参：无
- 返回（text 为 JSON，`structuredContent` 含同构字段）：

| 字段 | 类型 | 说明 |
|---|---|---|
| `logged_in` | boolean | 是否已登录 |
| `message` | string | 状态描述；未登录时包含可操作指引 |
| `url` | string | 检测时页面的 URL |

多重验证（全程只读，不点击不输入，**不会触发风控**，毫秒级）：

1. **URL 跳转检测**：页面被跳到 login 页 → 未登录；命中 punish/captcha → 风控提示
2. **Cookie 凭证**：是否含淘宝系登录 token（`unb`/`cn`/`_tb_token_`/`cookie2` 等）
3. **页面登录入口**：是否显示「请登录」类文案（未登录信号）
4. **昵称/头像可见性**：Cookie 昵称（`cn`）是否出现在页面文本、头像元素是否存在（已登录信号）

判定规则：无 Cookie 凭证 → 未登录；有凭证但页面仍显示「请登录」且无昵称/头像 → 判定登录态可能过期；其余 → 已登录。首页 URL 本身不体现登录态，不单独作为依据。

```json
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"check_login_status","arguments":{}}}
```

返回示例：

```json
{"logged_in":true,"message":"已登录（Cookie 登录凭证正常，页面无「请登录」入口）","url":"https://www.1688.com/"}
```

## 4. 工具参考：search_goods

### 入参（arguments）

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `keyword` | string | 是 | 搜索关键词，如 `"精致马克杯"` |
| `sort_type` | string | 否 | 排序：`default`（默认综合，搜索后的原始结果）/ `sales`（按销量）/ `price_asc`（价格升序），默认 `default`。`sales` 通过点击页面排序栏「销量」按钮生效（URL 参数不可靠），并等待结果刷新 |
| `price_min` | number | 否 | 最低价格（元），0 表示不限。通过页面「¥最低价」输入框填入并点击确认按钮生效 |
| `price_max` | number | 否 | 最高价格（元），0 表示不限。通过页面「¥最高价」输入框填入并点击确认按钮生效 |
| `only_dropshipping` | boolean | 否 | `true` 勾选页面「一件代发」复选框，只看支持一件代发的商品 |
| `page` | int | 否 | 页码，从 1 开始，默认第 1 页（每页约 60 个） |

`sort_type=sales`、`price_min/price_max`、`only_dropshipping` 均通过**模拟页面控件交互**实现（点排序栏「销量」按钮、勾选复选框、填价格输入框后点确认按钮；1688 忽略非受信的 JS 点击），并等待结果区真正刷新后才提取，不会静默返回未生效的数据。不传 `sort_type` 或传 `default` 时不做任何排序操作，直接使用搜索后的默认综合排序结果。注意：1688 价格筛选按最小规格单价过滤，个别按整盒/整批展示价格的商品（如盲盒）可能显示价略超区间，属 1688 侧展示差异；销量排序下前排可能有广告坑位且类目相关性让位于销量。

### 调用示例（原始 JSON-RPC）

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "search_goods",
    "arguments": { "keyword": "精致马克杯", "sort_type": "sales" }
  }
}
```

### 输出格式

结果在 `result.content[0].text`，为模型友好的纯文本：

```
找到 60 个商品：

1. ✅翟墨x联名系列景德镇陶瓷马克杯高颜值咖啡杯轻奢礼物送闺
   价格: ¥305.85
   月销量: 0
   店铺: 龙岩市新罗区迪迪克百货商行
   标签: 礼物、轻奢、联名
   主图: https://cbu01.alicdn.com/img/ibank/...webp
   链接: http://detail.m.1688.com/page/index.html?offerId=1075823819062&...

2. ...
```

- 无结果时返回：`未找到相关商品`（正常情况，不是错误）
- `月销量` 为 0 表示该卡片未展示销量（1688 仅在部分卡片上展示"已售xxx件"），非数据错误
- `标签` 行是卡片上的类目/风格/优惠特征，可用于过滤弱相关商品（见下「数据准确性」）

### 数据准确性说明（重要）

| 现象 | 原因 | Agent 侧建议 |
|---|---|---|
| 个别价格异常高（如 ¥10000） | 1688 结果页前排坑位卡真实显示的价格（页面本身如此，非解析错误） | 按价格区间过滤，或结合同类商品中位数剔除离群值 |
| 大部分商品月销量为 0 | 1688 页面只在少数卡片展示销量文字 | 关注销量 > 0 的商品；对销量为 0 的商品用价格/标签判断 |
| 按销量排序混入弱相关商品 | 销量排序下 1688 让位于销量，类目相关性下降；前排可能有广告坑位 | 用 `default` 排序提高相关性；或用 `标签` 行过滤（如要求含品类词） |
| 少数店铺为空 | 部分卡片页面上不显示店铺名 | 忽略该字段即可，不影响其他数据 |

### Agent 解析建议

逐条解析「序号. 标题 / 价格 / 月销量 / 店铺 / 主图 / 链接」行结构；`链接` 可直接传入 `get_goods_detail` 的 `goods_url` 查看单个商品详情。

## 5. 工具参考：get_goods_detail

获取单个商品的完整详情。入参 `goods_url`（商品链接，m 站/PC 站均可，服务端自动归一）或 `goods_id`（offerId 纯数字），至少提供一个；内部会先检查登录状态，未登录直接报错提示先登录。

```json
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_goods_detail","arguments":{"goods_id":"733436976467"}}}
```

返回字段（`content[0].text` 为排版文本，`structuredContent.detail` 为结构化 JSON）：

| 字段 | 类型 | 说明 |
|---|---|---|
| `offer_id` / `url` | string | 商品 ID 与归一化后的 PC 详情页链接 |
| `title` / `unit` | string | 标题 / 计量单位（个/件/袋…） |
| `price_ranges` | array | 阶梯批发价 `[{price, begin_amount}]`（如 满20件 ¥0.08） |
| `begin_num` | int | 最小起批量 |
| `mix_number` | int | 支持混批时的最少混批件数，0=不支持混批 |
| `total_stock` | int | 总可售库存 |
| `sku_summary` | object | SKU 摘要：`count`（SKU 总数）、`props`（规格维度名+可选值，值最多前 50 个并注明 total）、`price_min/price_max`（单 SKU 价格区间）、`stock_min/stock_max`（单 SKU 库存区间）。**不逐条返回 SKU 明细** |
| `main_images` | array | 主图列表（含规格图，全部返回） |
| `detail_images` | array | 详情区长图，最多前 20 张 |
| `supplier` | object | 公司名 `name`、登录账号 `login_id`、店铺链接 `shop_url`、标签 `tags`（超级工厂/实力商家/安心购等，页面信号推导） |
| `location` | string | 产地 |
| `attributes` | array | 参数表（已剔除与 SKU 维度重复的 颜色/尺寸 项） |
| `sale_num` / `sale_period` / `sale_count` | | 展示销量（"10万+"）/ 统计周期（"一年内"）/ 精确数字（页面提供时） |
| `description_url` | string | 详情描述数据源 URL（调试用） |
| `warning` | string | 内嵌数据缺失降级为 DOM 提取时的提示（此时字段可能不全） |

实现说明（仅供排查）：数据主要来自详情页内嵌 `window.context` JSON（结构化、稳定），`context` 缺失时自动降级为 DOM 文本正则提取并附 `warning`。

## 6. 错误对照表

业务错误统一为 JSON-RPC error，`code: -32000`：

| 错误信息（原文） | 含义与处理 |
|---|---|
| `keyword 不能为空` | 补充 keyword 重试 |
| `sort_type 仅支持 default / sales / price_asc，当前为 "xxx"` | 修正 sort_type |
| `price_min（5）不能大于 price_max（3）` | 修正价格区间 |
| `无法解析商品ID：…` / `商品ID格式不正确…` | get_goods_detail：确认传了 goods_url（含 offerId= 或 /offer/xxx.html）或纯数字 goods_id |
| `未登录，无法获取完整商品详情: …` | get_goods_detail 内置登录检查未通过：提示用户在有头窗口登录后重试 |
| `1688 跳转到了登录页，请先在有头模式下启动服务并手动登录一次（登录态保存在 user_data 目录），再重试` | 登录态失效，按第 1 节重新登录 |
| `1688 触发了风控验证，请在浏览器窗口中手动完成验证后重试` | 服务已自动等待 60 秒仍未通过；在有头窗口拖滑块后再重试，并降低调用频率 |
| `等待搜索结果超时（20s）` | 页面加载慢或结构临时异常，稍后重试 |
| `详情页数据未加载完成…` | 详情页结构临时异常，稍后重试；反复出现需校准 detail.go |
| `未提取到搜索结果，可能 1688 页面结构已变化…` | 1688 改版，需运行 `tools/dump` 重新校准选择器 |
| `打开搜索页失败: ...` / `打开详情页失败: ...` | 浏览器异常，重启服务 |

参数错误为 `-32602`，未知工具为 `-32601`。

## 7. 运行行为与注意事项

- **耗时**：单次搜索典型 20~60 秒（导航 + 反爬停顿 + 懒加载滚动 + 提取）；单次详情典型 10~25 秒；**批量取详情时相邻两次强制间隔 ≥`detail_interval`（默认 30 秒，config.yaml 可调；间隔不足服务端自动等待），第二次起耗时增加等待时长**。客户端超时建议 ≥180 秒。
- **超时重试**：导航/等待类超时服务会自动换新页面重试 1 次（最坏耗时约翻倍）；风控/登录拦截、空结果、页面结构变化不会重试。
- **风控**：频繁搜索/抓详情会触发 1688 滑块验证。服务检测到风控/登录页会自动等待人工处理（默认 60 秒，`captcha_wait` 可调），期间在有头窗口拖滑块即可，通过后自动继续提取。**`get_goods_detail` 服务端强制限速：相邻两次调用间隔不足 `detail_interval`（默认 30 秒，config.yaml 可调，风控严调大/风控松调小）时自动等待补足（日志可见），批量获取详情无需 agent 自己计时**；搜索建议自行间隔 ≥30 秒。
- **登录态**：保存在 `./user_data`，删除该目录 = 清除登录态。Agent 流程要求：**所有操作前先调用 `check_login_status`**，`logged_in=false` 时提示用户在有头浏览器窗口完成登录，不要继续调用 `search_goods` / `get_goods_detail`。
- **并发**：服务为单浏览器串行抓取，避免并发调用同一服务。

## 8. 故障排查

| 现象 | 排查 |
|---|---|
| 启动报 `Failed to get the debug url: exit status 1` 或「登录态目录已被另一个 Chrome 实例占用（PID: ...）」 | 同一时间只能运行**一个** mcp-1688 服务：先关闭旧实例（任务管理器结束 `mcp-1688.exe` 及报错里列出的 chrome.exe），再启动。agent 与手动调试不要同时启动 |
| 端口被占用 | 换 `PORT` 环境变量 |
| 搜索全部月销量为 0 | 正常，关键词下卡片无销量展示；换个热门词验证（如「钥匙扣」） |
| 连续超时/风控提示 | 在有头窗口完成滑块验证；降低调用频率；必要时删除 `./user_data` 重新登录 |
| 启动无浏览器窗口 | 正常（rod 默认不开启动窗口），有头模式会自动打开 1688 首页标签页 |
| 本机无 Chrome 时首次启动慢 | rod 在下载托管 Chromium（约 150MB），只需一次 |
