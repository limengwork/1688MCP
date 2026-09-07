# 1688 MCP Server

[中文文档](README_CN.md) | English

A Model Context Protocol (MCP) server for 1688 (Alibaba's wholesale platform). Search products, get details, and extract data using browser automation.

## Features

- 🔍 **Product Search** - Search products with filters (price, dropshipping, sort by sales)
- 📦 **Product Details** - Get complete product information (pricing, SKUs, inventory, images)
- 🔐 **Login Detection** - Automatic login status checking
- 🛡️ **Anti-bot Protection** - Built-in captcha detection and waiting
- 💾 **Session Persistence** - Login state saved across restarts

## Quick Start

### Prerequisites

- Windows 10/11
- Go 1.21+ (for building from source)

### Download

Download the latest release from [Releases](https://github.com/yourusername/1688-mcp/releases)

### Build from Source

```bash
git clone https://github.com/yourusername/1688-mcp.git
cd 1688-mcp
go build -o mcp-1688.exe .
```

### Run

```bash
# First run (will open browser for login)
.\mcp-1688.exe

# Headless mode (after first login)
set HEADLESS=1
.\mcp-1688.exe
```

## Usage

### 1. Login (First Time)

Start the server in headed mode:
```bash
.\mcp-1688.exe
```

A browser window will open. Log in to your 1688 account manually. The login state will be saved in `./user_data/`.

### 2. Configure MCP Client

Add to your MCP client configuration:

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

### 3. Use Tools

#### Check Login Status
```json
{"name": "check_login_status", "arguments": {}}
```

#### Search Products
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

#### Get Product Details
```json
{
  "name": "get_goods_detail",
  "arguments": {
    "goods_id": "123456789"
  }
}
```

## Tools

| Tool | Description |
|------|-------------|
| `check_login_status` | Check if logged in to 1688 |
| `search_goods` | Search products with filters |
| `get_goods_detail` | Get detailed product information |

## Configuration

Edit `config.yaml`:

```yaml
port: "18080"
headless: false
user_data_dir: ./user_data

timeouts:
  result_wait: 20
  captcha_wait: 60
  page_timeout: 90
```

Or use environment variables:
```bash
set PORT=19090
set HEADLESS=1
```

## Troubleshooting

| Issue | Solution |
|-------|----------|
| Firewall warning | Use compiled exe instead of `go run .` |
| Login expired | Delete `user_data/` and re-login |
| Captcha triggered | Complete captcha in browser window |
| Search timeout | Check internet connection, try again |

## License

MIT License - see [LICENSE](LICENSE) for details

## Acknowledgments

- [rod](https://github.com/go-rod/rod) - Browser automation library
- [MCP Protocol](https://modelcontextprotocol.io/) - Model Context Protocol
