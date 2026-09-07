package main

import (
	"encoding/json"
	"net/http"
	"slices"
)

// MCPServer 实现 MCP 协议（JSON-RPC 2.0 over HTTP）。
type MCPServer struct {
	searchService *SearchService
	detailService *DetailService
}

func NewMCPServer(searchService *SearchService, detailService *DetailService) *MCPServer {
	return &MCPServer{searchService: searchService, detailService: detailService}
}

// JSONRPCRequest 请求体。ID 用 RawMessage 是为了区分"没有 id"（通知）和 id=null。
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// JSONRPCResponse 响应体。
type JSONRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
}

// RPCError JSON-RPC 错误对象。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// id 解析请求 ID，用于在响应中回传。
func (r JSONRPCRequest) id() interface{} {
	var v interface{}
	if len(r.ID) > 0 {
		_ = json.Unmarshal(r.ID, &v)
	}
	return v
}

// supportedVersions 本服务支持的 MCP 协议版本，客户端请求的版本在列时回显它。
var supportedVersions = []string{"2024-11-05", "2025-03-26", "2025-06-18"}

// HandleHTTP 处理 /mcp 端点。
func (s *MCPServer) HandleHTTP(w http.ResponseWriter, r *http.Request) {
	// MCP streamable HTTP 只用 POST 传请求；GET（SSE 流）对无状态服务返回 405
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req JSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.sendError(w, nil, -32700, "Parse error")
		return
	}

	// 没有 ID 的是通知（如 notifications/initialized），按规范回 202、不带响应体
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		s.handleInitialize(w, req)
	case "tools/list":
		s.handleToolsList(w, req)
	case "tools/call":
		s.handleToolsCall(w, req)
	case "ping":
		s.sendResult(w, req.id(), map[string]interface{}{})
	default:
		s.sendError(w, req.id(), -32601, "Method not found")
	}
}

// handleInitialize 协议握手。
func (s *MCPServer) handleInitialize(w http.ResponseWriter, req JSONRPCRequest) {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &params)
	}

	version := "2024-11-05"
	if slices.Contains(supportedVersions, params.ProtocolVersion) {
		version = params.ProtocolVersion
	}

	s.sendResult(w, req.id(), map[string]interface{}{
		"protocolVersion": version,
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{},
		},
		"serverInfo": map[string]interface{}{
			"name":    "1688-mcp",
			"version": "1.0.0",
		},
	})
}

// handleToolsList 声明本服务提供的工具及入参 schema。
func (s *MCPServer) handleToolsList(w http.ResponseWriter, req JSONRPCRequest) {
	tools := []map[string]interface{}{
		{
			"name":        "check_login_status",
			"description": "检测 1688 登录状态（多重验证：登录页跳转 URL / Cookie 登录凭证 / 页面「请登录」入口 / 昵称头像可见性）。所有操作前必须先调用本工具",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			"name":        "search_goods",
			"description": "搜索 1688 商品，返回商品列表（调用前请先 check_login_status）",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"keyword": map[string]interface{}{
						"type":        "string",
						"description": "搜索关键词",
					},
					"sort_type": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"default", "sales", "price_asc"},
						"description": "排序方式",
					},
					"price_min": map[string]interface{}{
						"type":        "number",
						"description": "最低价格",
					},
					"price_max": map[string]interface{}{
						"type":        "number",
						"description": "最高价格",
					},
					"only_dropshipping": map[string]interface{}{
						"type":        "boolean",
						"description": "仅一件代发",
					},
					"page": map[string]interface{}{
						"type":        "integer",
						"description": "页码",
					},
				},
				"required": []string{"keyword"},
			},
		},
		{
			"name":        "get_goods_detail",
			"description": "获取 1688 商品详情：标题、阶梯批发价、起批量、SKU 摘要（规格/价格/库存区间）、主图与详情图、供应商、产地、参数表。入参 goods_url（商品链接，m 站/PC 站均可）或 goods_id（offerId）至少一个。调用前请先 check_login_status",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"goods_url": map[string]interface{}{
						"type":        "string",
						"description": "商品详情页链接（detail.1688.com/offer/xxx.html 或含 offerId= 参数的链接）",
					},
					"goods_id": map[string]interface{}{
						"type":        "string",
						"description": "商品ID（offerId，纯数字）",
					},
				},
			},
		},
	}
	s.sendResult(w, req.id(), map[string]interface{}{"tools": tools})
}

func (s *MCPServer) sendResult(w http.ResponseWriter, id interface{}, result interface{}) {
	s.writeJSON(w, JSONRPCResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *MCPServer) sendError(w http.ResponseWriter, id interface{}, code int, message string) {
	s.writeJSON(w, JSONRPCResponse{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: message}})
}

func (s *MCPServer) writeJSON(w http.ResponseWriter, resp JSONRPCResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
