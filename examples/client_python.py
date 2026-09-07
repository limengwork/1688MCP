# 1688 MCP 客户端接入示例（官方 Python SDK 版）
#
# 使用 MCP 官方 Python SDK 的 Streamable HTTP 传输接入，
# 适合已在使用 mcp SDK 的项目（如 FastMCP 生态、Agent 框架）。
#
# 安装依赖：pip install mcp
# 运行前提：服务已启动（默认 http://localhost:18080/mcp）
# 运行：python examples/client_python.py
"""官方 mcp SDK 接入 1688 MCP 服务。"""

import asyncio

from mcp import ClientSession
from mcp.client.streamable_http import streamablehttp_client

MCP_URL = "http://localhost:18080/mcp"


async def main() -> None:
    # streamablehttp_client 返回 (读流, 写流, 回调)；会话须在连接内使用
    async with streamablehttp_client(MCP_URL) as (read, write, _):
        async with ClientSession(read, write) as session:
            # 1) 初始化握手
            await session.initialize()
            print("握手完成")

            # 2) 列出可用工具
            tools = await session.list_tools()
            for t in tools.tools:
                print(f"工具: {t.name} - {t.description}")

            # 3) 调用搜索
            result = await session.call_tool(
                "search_goods",
                {"keyword": "精致马克杯", "sort_type": "sales"},
            )
            for content in result.content:
                print(content.text)


if __name__ == "__main__":
    asyncio.run(main())
