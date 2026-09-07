package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// handleToolsCall 处理 tools/call 请求，按工具名分发。
func (s *MCPServer) handleToolsCall(w http.ResponseWriter, req JSONRPCRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.sendError(w, req.id(), -32602, "Invalid params")
		return
	}

	switch params.Name {
	case "check_login_status":
		s.callCheckLoginStatus(w, req)
	case "search_goods":
		s.callSearchGoods(w, req, params.Arguments)
	case "get_goods_detail":
		s.callGetGoodsDetail(w, req, params.Arguments)
	default:
		s.sendError(w, req.id(), -32601, "Tool not found: "+params.Name)
	}
}

// callGetGoodsDetail get_goods_detail 工具实现：解析入参 -> 抓取详情 -> 格式化输出。
func (s *MCPServer) callGetGoodsDetail(w http.ResponseWriter, req JSONRPCRequest, rawArgs json.RawMessage) {
	var args DetailOptions
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			s.sendError(w, req.id(), -32602, "参数解析失败: "+err.Error())
			return
		}
	}

	log.Printf("get_goods_detail: goods_id=%q goods_url=%.80s", args.GoodsID, args.GoodsURL)

	detail, err := s.detailService.GetGoodsDetail(args)
	if err != nil {
		log.Printf("get_goods_detail 失败: %v", err)
		s.sendError(w, req.id(), -32000, err.Error())
		return
	}

	payload := map[string]interface{}{"detail": detail}
	s.sendResult(w, req.id(), map[string]interface{}{
		// text 给模型阅读的排版文本；structuredContent 附完整结构化 JSON
		"content": []map[string]interface{}{
			{"type": "text", "text": formatGoodsDetail(detail)},
		},
		"structuredContent": payload,
	})
}

// formatGoodsDetail 把商品详情渲染成给模型看的文本。
func formatGoodsDetail(d *GoodsDetail) string {
	if d == nil {
		return "未获取到商品详情"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "商品详情（offerId=%s）\n", d.OfferID)
	fmt.Fprintf(&b, "标题: %s\n", d.Title)
	if d.Warning != "" {
		fmt.Fprintf(&b, "注意: %s\n", d.Warning)
	}

	// 价格：阶梯批发价
	if len(d.PriceRanges) > 0 {
		parts := make([]string, 0, len(d.PriceRanges))
		for _, r := range d.PriceRanges {
			parts = append(parts, fmt.Sprintf("满%d件 ¥%.2f", r.BeginAmount, r.Price))
		}
		fmt.Fprintf(&b, "批发价: %s\n", strings.Join(parts, "；"))
	}
	if d.BeginNum > 0 {
		line := fmt.Sprintf("起批量: %d", d.BeginNum)
		if d.Unit != "" {
			line += " " + d.Unit
		}
		if d.MixNumber > 0 {
			line += fmt.Sprintf("（支持混批，最少%d件）", d.MixNumber)
		}
		b.WriteString(line + "\n")
	}
	if d.TotalStock > 0 {
		fmt.Fprintf(&b, "总库存: %s%s\n", formatDetailNumber(d.TotalStock), d.Unit)
	}

	// SKU 摘要
	if d.SKU != nil && d.SKU.Count > 0 {
		fmt.Fprintf(&b, "SKU: 共 %d 个规格，单价 ¥%.2f~¥%.2f，单规格库存 %s~%s\n",
			d.SKU.Count, d.SKU.PriceMin, d.SKU.PriceMax,
			formatDetailNumber(d.SKU.StockMin), formatDetailNumber(d.SKU.StockMax))
		for _, p := range d.SKU.Props {
			v := strings.Join(p.Values, "、")
			if p.Total > len(p.Values) {
				v += fmt.Sprintf("（等共 %d 项）", p.Total)
			}
			fmt.Fprintf(&b, "  规格-%s: %s\n", p.Name, v)
		}
	}

	if d.SaleNum != "" {
		line := "销量: " + d.SaleNum
		if d.SalePeriod != "" {
			line += "（" + d.SalePeriod + "）"
		}
		line += d.Unit
		b.WriteString(line + "\n")
	}
	if d.Supplier != nil {
		line := "供应商: " + d.Supplier.Name
		if len(d.Supplier.Tags) > 0 {
			line += "（" + strings.Join(d.Supplier.Tags, "、") + "）"
		}
		b.WriteString(line + "\n")
		if d.Supplier.ShopURL != "" {
			fmt.Fprintf(&b, "店铺: %s\n", d.Supplier.ShopURL)
		}
	}
	if d.Location != "" {
		fmt.Fprintf(&b, "产地: %s\n", d.Location)
	}
	if len(d.Attributes) > 0 {
		pairs := make([]string, 0, len(d.Attributes))
		for _, a := range d.Attributes {
			pairs = append(pairs, a.Name+"="+a.Value)
		}
		fmt.Fprintf(&b, "参数: %s\n", strings.Join(pairs, "；"))
	}

	fmt.Fprintf(&b, "主图(%d张):\n", len(d.MainImages))
	for _, u := range d.MainImages {
		b.WriteString("  " + u + "\n")
	}
	if len(d.DetailImages) > 0 {
		fmt.Fprintf(&b, "详情图(前%d张):\n", len(d.DetailImages))
		for _, u := range d.DetailImages {
			b.WriteString("  " + u + "\n")
		}
	}
	fmt.Fprintf(&b, "链接: %s\n", d.URL)
	return b.String()
}

// callCheckLoginStatus check_login_status 工具实现：多重验证 1688 登录状态。
func (s *MCPServer) callCheckLoginStatus(w http.ResponseWriter, req JSONRPCRequest) {
	status, err := s.searchService.browser.CheckLoginStatus()
	if err != nil {
		log.Printf("check_login_status 失败: %v", err)
		s.sendError(w, req.id(), -32000, err.Error())
		return
	}
	log.Printf("check_login_status: logged_in=%v url=%s", status.LoggedIn, status.URL)

	payload := map[string]interface{}{
		"logged_in": status.LoggedIn,
		"message":   status.Message,
		"url":       status.URL,
	}
	raw, _ := json.Marshal(payload)
	s.sendResult(w, req.id(), map[string]interface{}{
		// text 直接回 JSON，方便 agent 解析；structuredContent 附结构化字段
		"content": []map[string]interface{}{
			{"type": "text", "text": string(raw)},
		},
		"structuredContent": payload,
	})
}

// callSearchGoods search_goods 工具实现：解析入参 -> 调用搜索服务 -> 格式化输出。
func (s *MCPServer) callSearchGoods(w http.ResponseWriter, req JSONRPCRequest, rawArgs json.RawMessage) {
	var args SearchOptions
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			s.sendError(w, req.id(), -32602, "参数解析失败: "+err.Error())
			return
		}
	}

	log.Printf("search_goods: keyword=%q sort_type=%q price=[%v,%v] page=%d",
		args.Keyword, args.SortType, args.PriceMin, args.PriceMax, args.Page)

	result, err := s.searchService.Search(args.Keyword, args)
	if err != nil {
		log.Printf("search_goods 失败: %v", err)
		s.sendError(w, req.id(), -32000, err.Error())
		return
	}

	s.sendResult(w, req.id(), map[string]interface{}{
		"content": []map[string]interface{}{
			{"type": "text", "text": formatSearchResult(result)},
		},
	})
}

// formatSearchResult 把搜索结果渲染成给模型看的文本。
func formatSearchResult(result *SearchResponse) string {
	if result == nil || len(result.Items) == 0 {
		return "未找到相关商品"
	}

	var output strings.Builder
	output.WriteString(fmt.Sprintf("找到 %d 个商品：\n\n", result.TotalCount))

	for i, item := range result.Items {
		output.WriteString(fmt.Sprintf("%d. %s\n", i+1, item.Title))
		output.WriteString(fmt.Sprintf("   价格: ¥%.2f\n", item.Price))
		output.WriteString(fmt.Sprintf("   月销量: %d\n", item.Sales))
		output.WriteString(fmt.Sprintf("   店铺: %s\n", item.ShopName))
		if len(item.Tags) > 0 {
			output.WriteString(fmt.Sprintf("   标签: %s\n", strings.Join(item.Tags, "、")))
		}
		output.WriteString(fmt.Sprintf("   主图: %s\n", item.ImageURL))
		output.WriteString(fmt.Sprintf("   链接: %s\n\n", item.URL))
	}

	return output.String()
}
