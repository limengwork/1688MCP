# 1688 MCP Server

1688（阿里巴巴批发平台）的 MCP 服务器。通过浏览器自动化搜索商品、获取详情、提取数据。

## 功能特性

- 🔍 **商品搜索** - 支持价格、一件代发、按销量排序等筛选
- 📦 **商品详情** - 获取完整商品信息（价格、SKU、库存、图片）
- 🔐 **登录检测** - 自动检测登录状态
- 🛡️ **反爬保护** - 内置验证码检测和等待
- 💾 **会话持久化** - 登录状态跨重启保存

## 快速开始

### 环境要求

- Windows 10/11
- Go 1.21+（从源码编译时需要）

### 下载

从 [Releases](https://github.com/limengwork/1688MCP/releases/tag/v1.0.0) 下载最新版本

### 从源码编译

```bash
git clone https://github.com/yourusername/1688-mcp.git
cd 1688-mcp
go build -o mcp-1688.exe .
```

### 运行

```bash
# 首次运行（会打开浏览器登录）
.\mcp-1688.exe

# 无头模式（首次登录后使用）
set HEADLESS=1
.\mcp-1688.exe
```

## 使用方法

### 1. 登录（首次）

有头模式启动服务：
```bash
.\mcp-1688.exe
```

浏览器会打开 1688 首页，手动登录账号。登录状态保存在 `./user_data/` 目录。

### 2. 配置 MCP 客户端

在 MCP 客户端配置中添加：

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

### 3. 使用工具

#### 检测登录状态
```json
{"name": "check_login_status", "arguments": {}}
```

#### 搜索商品
```json
{
  "name": "search_goods",
  "arguments": {
    "keyword": "马克杯",
    "sort_type": "sales",
    "only_dropshipping": true,
    "price_min": 10,
    "price_max": 100
  }
}
```

#### 获取商品详情
```json
{
  "name": "get_goods_detail",
  "arguments": {
    "goods_id": "123456789"
  }
}
```

## 工具列表

| 工具 | 说明 |
|------|------|
| `check_login_status` | 检测 1688 登录状态 |
| `search_goods` | 搜索商品（支持筛选） |
| `get_goods_detail` | 获取商品详情 |

## 配置说明

编辑 `config.yaml`：

```yaml
port: "18080"
headless: false
user_data_dir: ./user_data

timeouts:
  result_wait: 20
  captcha_wait: 60
  page_timeout: 90
```

或使用环境变量：
```bash
set PORT=19090
set HEADLESS=1
```

## 常见问题

| 问题 | 解决方案 |
|------|---------|
| 防火墙提醒 | 使用编译好的 exe，不要用 `go run .` |
| 登录过期 | 删除 `user_data/` 目录，重新登录 |
| 触发验证码 | 在浏览器窗口完成验证 |
| 搜索超时 | 检查网络连接，稍后重试 |

## 许可证

MIT License - 详见 [LICENSE](LICENSE)

## 致谢

- [rod](https://github.com/go-rod/rod) - 浏览器自动化库
- [MCP Protocol](https://modelcontextprotocol.io/) - 模型上下文协议
