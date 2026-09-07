// 1688 MCP 客户端接入示例（官方 TypeScript/Node SDK 版）
//
// 使用 MCP 官方 TS SDK 的 StreamableHTTPClientTransport 接入，
// 适合 Node.js 服务或 Agent 项目。
//
// 安装依赖：npm install @modelcontextprotocol/sdk
// 运行前提：服务已启动（默认 http://localhost:18080/mcp）
// 运行：node examples/client_node.mjs
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

const MCP_URL = "http://localhost:18080/mcp";

async function main() {
  // 1) 创建客户端并用 Streamable HTTP 传输连接
  const client = new Client({ name: "1688-demo-client", version: "1.0.0" });
  const transport = new StreamableHTTPClientTransport(new URL(MCP_URL));
  await client.connect(transport);
  console.log("握手完成");

  // 2) 列出可用工具
  const { tools } = await client.listTools();
  for (const t of tools) {
    console.log(`工具: ${t.name} - ${t.description}`);
  }

  // 3) 调用搜索
  const result = await client.callTool({
    name: "search_goods",
    arguments: { keyword: "精致马克杯", sort_type: "sales" },
  });
  for (const content of result.content) {
    console.log(content.text);
  }

  await client.close();
}

main().catch((err) => {
  console.error("调用失败:", err);
  process.exit(1);
});
