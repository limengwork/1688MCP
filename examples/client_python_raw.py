# 1688 MCP 客户端接入示例（零依赖版）
#
# 直接用 Python 标准库以 JSON-RPC 2.0 调用 1688 MCP 服务，
# 无需安装任何第三方包，适合轻量集成或快速验证。
#
# 运行前提：服务已启动（默认 http://localhost:18080/mcp）
#   go run . 或 mcp-1688.exe
# 运行：python examples/client_python_raw.py
"""零依赖 JSON-RPC 直连 1688 MCP 服务。"""

import json
import urllib.request

MCP_URL = "http://localhost:18080/mcp"


def rpc(method: str, params: dict | None = None, rpc_id: int = 1) -> dict:
    """发送一条 JSON-RPC 2.0 请求并返回响应。"""
    payload = {"jsonrpc": "2.0", "id": rpc_id, "method": method}
    if params is not None:
        payload["params"] = params
    req = urllib.request.Request(
        MCP_URL,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=300) as resp:
        return json.loads(resp.read().decode("utf-8"))


def main() -> None:
    # 1) 握手（可选，服务端无状态，但建议先 initialize 确认服务在线）
    init = rpc("initialize", {"protocolVersion": "2024-11-05", "capabilities": {}})
    info = init["result"]["serverInfo"]
    print(f"已连接: {info['name']} v{info['version']}")

    # 2) 列出可用工具
    tools = rpc("tools/list")["result"]["tools"]
    for t in tools:
        print(f"工具: {t['name']} - {t['description']}")

    # 3) 调用搜索
    result = rpc("tools/call", {
        "name": "search_goods",
        "arguments": {"keyword": "精致马克杯", "sort_type": "sales"},
    })
    if "error" in result:
        print("搜索失败:", result["error"]["message"])
        return
    print(result["result"]["content"][0]["text"])


if __name__ == "__main__":
    main()
