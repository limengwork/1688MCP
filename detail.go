package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
)

// DetailOptions MCP 工具 get_goods_detail 的入参。
// goods_url 与 goods_id 至少提供一个；都有时优先 goods_id。
type DetailOptions struct {
	GoodsURL string `json:"goods_url,omitempty"`
	GoodsID  string `json:"goods_id,omitempty"`
}

// PriceRange 阶梯批发价的一档。
type PriceRange struct {
	Price       float64 `json:"price"`
	BeginAmount int     `json:"begin_amount"` // 该档价对应的最低采购量
}

// SkuProp 一个规格维度（如「颜色」）及其可选值。
type SkuProp struct {
	Name   string   `json:"name"`
	Values []string `json:"values"` // 最多前 50 个
	Total  int      `json:"total"`  // 全部可选值数量
}

// SkuSummary SKU 摘要（不逐条返回 SKU，见 USAGE.md）。
type SkuSummary struct {
	Count    int       `json:"count"` // SKU 总数
	Props    []SkuProp `json:"props"`
	PriceMin float64   `json:"price_min"`
	PriceMax float64   `json:"price_max"`
	StockMin int64     `json:"stock_min"` // 单 SKU 最低库存
	StockMax int64     `json:"stock_max"` // 单 SKU 最高库存
}

// SupplierInfo 供应商信息。
type SupplierInfo struct {
	Name    string   `json:"name"`
	LoginID string   `json:"login_id,omitempty"`
	ShopURL string   `json:"shop_url,omitempty"`
	Tags    []string `json:"tags,omitempty"` // 工厂/实力商家等标签（页面信号推导）
}

// Attr 商品参数表的一行。
type Attr struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// GoodsDetail get_goods_detail 的返回结构。
type GoodsDetail struct {
	OfferID      string        `json:"offer_id"`
	URL          string        `json:"url"`
	Title        string        `json:"title"`
	Unit         string        `json:"unit,omitempty"`
	PriceRanges  []PriceRange  `json:"price_ranges,omitempty"` // 阶梯批发价，升序
	BeginNum     int           `json:"begin_num,omitempty"`    // 最小起批量
	TotalStock   int64         `json:"total_stock,omitempty"`  // 全店该商品总可售
	MixNumber    int           `json:"mix_number,omitempty"`   // 支持混批时的最少混批件数（0=不支持）
	SKU          *SkuSummary   `json:"sku_summary,omitempty"`
	MainImages   []string      `json:"main_images,omitempty"`
	DetailImages []string      `json:"detail_images,omitempty"` // 详情长图，最多前 20 张
	Supplier     *SupplierInfo `json:"supplier,omitempty"`
	Location     string        `json:"location,omitempty"` // 产地/发货地
	Attributes   []Attr        `json:"attributes,omitempty"`
	SaleNum      string        `json:"sale_num,omitempty"`    // 页面展示销量，如 "10万+"
	SalePeriod   string        `json:"sale_period,omitempty"` // 销量统计周期，如 "一年内"
	SaleCount    int64         `json:"sale_count,omitempty"`  // 精确销量数字（页面提供时）
	DescURL      string        `json:"description_url,omitempty"`
	Warning      string        `json:"warning,omitempty"` // 部分字段缺失时的说明
}

// DetailService 封装 1688 商品详情抓取逻辑。
type DetailService struct {
	browser   *BrowserManager
	cfg       *Config
	lastStart time.Time // 上次详情抓取的开始时间（防风控间隔用）
}

// detailMinInterval 相邻两次详情抓取的最小间隔，防止触发 1688 风控。
// 服务端强制执行：间隔不足时自动等待补足，而不是报错让调用方重试。
const detailMinInterval = 30 * time.Second

func NewDetailService(bm *BrowserManager, cfg *Config) *DetailService {
	return &DetailService{browser: bm, cfg: cfg}
}

// detailContextPollJS 轮询详情页内嵌数据是否就绪。
const detailContextPollJS = `() => !!(window.context && window.context.result && window.context.result.data)`

// detailExtractJS 从详情页提取结构化数据。
// 数据源优先级：window.context（页面内嵌 JSON，实测约 180KB，含全部字段）；
// context 不存在时退回 DOM 文本正则（字段变 raw_*，由 Go 侧标记 Warning）。
// 全程只读，不点击不输入。
const detailExtractJS = `() => {
	const out = {};
	const ctx = window.context;
	const data = ctx && ctx.result && ctx.result.data;
	if (data) {
		out.source = 'context';
		const pt = (data.productTitle || {}).fields || {};
		const mp = (data.mainPrice || {}).fields || {};
		const gal = (data.gallery || {}).fields || {};
		const desc = (data.description || {}).fields || {};
		const dj = (((data.Root || {}).fields || {}).dataJson) || {};
		const op = dj.orderParamModel ? (dj.orderParamModel.orderParam || {}) : {};
		const sm = dj.skuModel || {};
		const tm = dj.tempModel || {};
		const shop = pt.shopInfo || {};
		const signs = dj.sellerSign ? (dj.sellerSign.signs || {}) : {};

		out.title = pt.title || (document.title || '').replace(/ ?- 阿里巴巴$/, '');
		out.unit = pt.unit || tm.offerUnit || '';
		out.sale_num = pt.saleNum || '';
		out.sale_period = pt.saleCountDate || '';
		out.sale_count = tm.saledCount || 0;

		// 阶梯价：优先当前生效价，退回 URL 参数价/去促销价
		let ranges = [];
		if (mp.priceModel && Array.isArray(mp.priceModel.currentPrices) && mp.priceModel.currentPrices.length) {
			ranges = mp.priceModel.currentPrices;
		} else if (op.skuParam && Array.isArray(op.skuParam.skuRangePrices)) {
			ranges = op.skuParam.skuRangePrices;
		} else if (Array.isArray(mp.originalPricesWithoutPromotion)) {
			ranges = mp.originalPricesWithoutPromotion;
		}
		out.price_ranges = ranges.map(r => ({
			price: parseFloat(r.price) || 0,
			begin_amount: parseInt(r.beginAmount, 10) || 0
		}));
		out.begin_num = op.beginNum || 0;
		out.total_stock = op.canBookedAmount || 0;
		const mix = dj.mixModel || {};
		out.mix_number = mix.isSupportMix ? (mix.mixNumber || 0) : 0;

		// SKU 摘要
		const skuMap = sm.skuInfoMap || {};
		const skus = Object.keys(skuMap).map(k => skuMap[k]);
		const prices = skus.map(s => parseFloat((s.price != null ? s.price : s.discountPrice))).filter(v => !isNaN(v));
		const stocks = skus.map(s => parseInt(s.canBookCount, 10) || 0);
		out.sku = {
			count: skus.length,
			props: (sm.skuProps || []).map(p => ({
				name: p.prop,
				values: (p.value || []).map(v => v.name).filter(Boolean),
				total: (p.value || []).length
			})),
			price_min: prices.length ? Math.min.apply(null, prices) : 0,
			price_max: prices.length ? Math.max.apply(null, prices) : 0,
			stock_min: stocks.length ? Math.min.apply(null, stocks) : 0,
			stock_max: stocks.length ? Math.max.apply(null, stocks) : 0
		};

		// 主图（过滤图标类资源）
		const isGoodsImg = u => /cbu01\.alicdn\.com\/img\/ibank\//.test(u);
		const seen = {};
		out.main_images = (gal.offerImgList || gal.mainImage || [])
			.filter(u => u && isGoodsImg(u) && !seen[u] && (seen[u] = 1));

		// 供应商
		const tags = [];
		if (shop.isPmSource || shop.isPm) tags.push('超级工厂');
		if (signs.isFactoryDealer) tags.push('工厂');
		if (signs.isSlsj) tags.push('实力商家');
		if (signs.isEaseBuyDealer || dj.easeBuyDealer && dj.easeBuyDealer.isEaseBuyDealer) tags.push('安心购');
		out.supplier = {
			name: shop.companyName || tm.companyName || '',
			login_id: tm.sellerLoginId || '',
			shop_url: (dj.offerBaseInfo && dj.offerBaseInfo.sellerWinportUrl) || tm.winportUrl || '',
			tags: tags
		};

		// 参数表（CpvEnhance：decisionCpv + normalCpv）
		const cpv = gal.CpvEnhance || {};
		out.attributes = [].concat(cpv.decisionCpv || [], cpv.normalCpv || [])
			.filter(a => a && a.name)
			.map(a => ({ name: a.name, value: (a.values || []).join('、') }));

		out.description_url = desc.detailUrl || '';
		out.page_url = location.href;
		return JSON.stringify(out);
	}

	// DOM 兜底：内嵌数据缺失（页面改版/异常）时按文本正则提取
	out.source = 'dom';
	const t = document.body ? document.body.innerText : '';
	out.title = (document.title || '').replace(/ ?- 阿里ibaba$/, '').replace(/ ?- 阿里巴巴$/, '');
	out.price_ranges = null;
	const pm = t.match(/¥[\d.]+(\s*[-~]\s*¥?[\d.]+)?/);
	if (pm) out.price_text = pm[0];
	const mm = t.match(/(\d+)\s*个?起批/);
	if (mm) out.begin_num_text = mm[0];
	const sm2 = t.match(/库存[\d.万亿]+个?/);
	if (sm2) out.stock_text = sm2[0];
	const sup = t.match(/[\u4e00-\u9fa5（）()]+(有限公司|厂|贸易|实业|商行)/);
	if (sup) out.supplier_text = sup[0];
	const loc = t.match(/产地\s*[:：]?\s*([\u4e00-\u9fa5]{2,12})/);
	if (loc) out.location_text = loc[1];
	out.page_url = location.href;
	return JSON.stringify(out);
}`

// offerIDPattern 详情页 URL 中提取 offerId 的正则（PC 链接 /offer/xxx.html）
var offerIDPattern = regexp.MustCompile(`/offer/(\d+)\.html`)

// extractOfferID 从 goods_url / goods_id 中解析 offerId。
func extractOfferID(opts DetailOptions) (string, error) {
	id := strings.TrimSpace(opts.GoodsID)
	if id == "" && opts.GoodsURL != "" {
		u := opts.GoodsURL
		if i := strings.Index(u, "offerId="); i >= 0 {
			rest := u[i+len("offerId="):]
			if end := strings.IndexAny(rest, "&#"); end >= 0 {
				rest = rest[:end]
			}
			id = rest
		} else if m := offerIDPattern.FindStringSubmatch(u); m != nil {
			id = m[1]
		}
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.New("无法解析商品ID：goods_url 或 goods_id 至少提供一个有效的（offerId 为纯数字，可从商品链接 offerId= 参数或 /offer/xxx.html 中获取）")
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return "", fmt.Errorf("商品ID格式不正确（应为纯数字 offerId），当前 %q", id)
		}
	}
	return id, nil
}

// GetGoodsDetail 抓取商品详情。
// 超时自动重试一次（与搜索一致的策略）；风控/登录拦截等待人工处理后继续。
func (s *DetailService) GetGoodsDetail(opts DetailOptions) (*GoodsDetail, error) {
	offerID, err := extractOfferID(opts)
	if err != nil {
		return nil, err
	}

	// 登录前置检查：未登录时详情页价格/库存可能显示不全
	status, err := s.browser.CheckLoginStatus()
	if err != nil {
		return nil, fmt.Errorf("登录状态检查失败: %w", err)
	}
	if !status.LoggedIn {
		return nil, errors.New("未登录，无法获取完整商品详情: " + status.Message)
	}

	// 防风控：距上次详情抓取不足最小间隔时自动等待补足（批量获取详情时保证调用间隔）。
	// 间隔时长来自配置 detail_interval（默认 30 秒），风控严可调大、风控松可调小。
	if !s.lastStart.IsZero() {
		if wait := s.cfg.DetailInterval - time.Since(s.lastStart); wait > 0 {
			log.Printf("距上次详情抓取间隔不足 %s，等待 %s 规避风控...", s.cfg.DetailInterval, wait.Round(time.Second))
			time.Sleep(wait)
		}
	}

	var detail *GoodsDetail
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		detail, lastErr = s.runGetDetail(offerID)
		if lastErr == nil || !isTimeoutError(lastErr) {
			break
		}
		if attempt == 1 {
			log.Printf("详情抓取超时: %v，自动重试一次", lastErr)
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	detail.OfferID = offerID
	return detail, nil
}

// runGetDetail 单次「打开详情页 → 等数据就绪 → 提取 → 补详情图」流程。
func (s *DetailService) runGetDetail(offerID string) (*GoodsDetail, error) {
	s.lastStart = time.Now() // 记录抓取开始时间，供下次调用计算防风控间隔
	pageURL := "https://detail.1688.com/offer/" + offerID + ".html"
	page, err := s.browser.NewPage(pageURL)
	if err != nil {
		return nil, fmt.Errorf("打开详情页失败: %w", err)
	}
	defer page.Close()
	page = page.Timeout(s.cfg.PageTimeout)

	randomSleep(1, 2)

	// 等内嵌数据就绪（m 站跳转 + 渲染约需数秒）
	if !s.waitContextReady(page, 20*time.Second) {
		if hint := detectBlock(page); hint != "" {
			if werr := s.waitHumanSolveDetail(page); werr != nil {
				return nil, werr
			}
		} else {
			return nil, fmt.Errorf("详情页数据未加载完成（可能页面结构变化或网络异常），URL: %s", pageURL)
		}
	}

	res, err := page.Eval(detailExtractJS)
	if err != nil {
		return nil, fmt.Errorf("提取详情数据失败: %w", err)
	}

	var raw struct {
		Source      string       `json:"source"`
		Title       string       `json:"title"`
		Unit        string       `json:"unit"`
		SaleNum     string       `json:"sale_num"`
		SalePeriod  string       `json:"sale_period"`
		SaleCount   int64        `json:"sale_count"`
		PriceRanges []PriceRange `json:"price_ranges"`
		BeginNum    int          `json:"begin_num"`
		TotalStock  int64        `json:"total_stock"`
		MixNumber   int          `json:"mix_number"`
		SKU         *struct {
			Count int `json:"count"`
			Props []struct {
				Name   string   `json:"name"`
				Values []string `json:"values"`
				Total  int      `json:"total"`
			} `json:"props"`
			PriceMin float64 `json:"price_min"`
			PriceMax float64 `json:"price_max"`
			StockMin int64   `json:"stock_min"`
			StockMax int64   `json:"stock_max"`
		} `json:"sku"`
		MainImages []string `json:"main_images"`
		Supplier   *struct {
			Name    string   `json:"name"`
			LoginID string   `json:"login_id"`
			ShopURL string   `json:"shop_url"`
			Tags    []string `json:"tags"`
		} `json:"supplier"`
		Attributes     []Attr `json:"attributes"`
		DescriptionURL string `json:"description_url"`
		PageURL        string `json:"page_url"`
		// DOM 兜底字段
		PriceText    string `json:"price_text"`
		BeginNumText string `json:"begin_num_text"`
		StockText    string `json:"stock_text"`
		SupplierText string `json:"supplier_text"`
		LocationText string `json:"location_text"`
	}
	if err := json.Unmarshal([]byte(res.Value.Str()), &raw); err != nil {
		return nil, fmt.Errorf("解析详情数据失败: %w", err)
	}

	detail := &GoodsDetail{
		URL: pageURL,
	}
	if raw.Source != "context" {
		detail.Warning = "页面未提供内嵌数据，以下为 DOM 文本兜底提取，字段可能不全"
	}
	detail.Title = raw.Title
	detail.Unit = raw.Unit
	detail.SaleNum = raw.SaleNum
	detail.SalePeriod = raw.SalePeriod
	detail.SaleCount = raw.SaleCount
	detail.PriceRanges = raw.PriceRanges
	detail.BeginNum = raw.BeginNum
	detail.TotalStock = raw.TotalStock
	detail.MixNumber = raw.MixNumber
	detail.DescURL = raw.DescriptionURL

	if raw.SKU != nil && raw.SKU.Count > 0 {
		props := make([]SkuProp, 0, len(raw.SKU.Props))
		for _, p := range raw.SKU.Props {
			vals := p.Values
			if len(vals) > 50 { // 用户约定：规格值最多返回前 50 个
				vals = vals[:50]
			}
			props = append(props, SkuProp{Name: p.Name, Values: vals, Total: p.Total})
		}
		detail.SKU = &SkuSummary{
			Count:    raw.SKU.Count,
			Props:    props,
			PriceMin: raw.SKU.PriceMin,
			PriceMax: raw.SKU.PriceMax,
			StockMin: raw.SKU.StockMin,
			StockMax: raw.SKU.StockMax,
		}
	}
	if raw.Supplier != nil && (raw.Supplier.Name != "" || raw.Supplier.ShopURL != "") {
		detail.Supplier = &SupplierInfo{
			Name:    raw.Supplier.Name,
			LoginID: raw.Supplier.LoginID,
			ShopURL: raw.Supplier.ShopURL,
			Tags:    raw.Supplier.Tags,
		}
	}
	// 参数表过滤：CpvEnhance 会把 SKU 维度（颜色/尺寸）也列进参数，
	// 与 SKU 摘要行重复，跳过名称与规格维度相同的项
	skuPropNames := map[string]bool{}
	if detail.SKU != nil {
		for _, p := range detail.SKU.Props {
			skuPropNames[p.Name] = true
		}
	}
	for _, a := range raw.Attributes {
		if skuPropNames[a.Name] {
			continue
		}
		detail.Attributes = append(detail.Attributes, a)
	}
	for _, a := range detail.Attributes {
		if a.Name == "产地" && a.Value != "" {
			detail.Location = a.Value
			break
		}
	}

	detail.MainImages = raw.MainImages
	if len(detail.MainImages) == 0 && raw.Source != "context" {
		detail.MainImages = collectDetailImagesFromDOM(page)
	}

	// DOM 兜底字段映射
	if detail.Warning != "" {
		if raw.PriceText != "" {
			detail.Attributes = append(detail.Attributes, Attr{Name: "价格(文本)", Value: raw.PriceText})
		}
		if raw.BeginNumText != "" {
			fmt.Sscanf(raw.BeginNumText, "%d", &detail.BeginNum)
			detail.Attributes = append(detail.Attributes, Attr{Name: "起批量(文本)", Value: raw.BeginNumText})
		}
		if raw.StockText != "" {
			detail.Attributes = append(detail.Attributes, Attr{Name: "库存(文本)", Value: raw.StockText})
		}
		if detail.Supplier == nil && raw.SupplierText != "" {
			detail.Supplier = &SupplierInfo{Name: raw.SupplierText}
		}
		if detail.Location == "" {
			detail.Location = raw.LocationText
		}
	}

	// 详情长图：从 description_url 拉取 HTML 提取图片（可选步骤，失败不影响主流程）
	if raw.DescriptionURL != "" {
		detail.DetailImages = fetchDescriptionImages(raw.DescriptionURL, 20)
	}

	return detail, nil
}

// waitContextReady 轮询详情页内嵌数据就绪；期间检测风控跳转。
func (s *DetailService) waitContextReady(page *rod.Page, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		res, err := page.Eval(detailContextPollJS)
		if err == nil && res.Value.Bool() {
			return true
		}
		if info, err := page.Info(); err == nil {
			u := info.URL
			if strings.Contains(u, "punish") || strings.Contains(u, "login") {
				return false
			}
		}
		time.Sleep(pollInterval)
	}
	return false
}

// waitHumanSolveDetail 命中风控后等待人工处理：轮询内嵌数据就绪或 URL 离开拦截页。
func (s *DetailService) waitHumanSolveDetail(page *rod.Page) error {
	log.Printf("详情页命中风控/登录拦截，等待人工处理（最长 %s）...", s.cfg.CaptchaWait)
	deadline := time.Now().Add(s.cfg.CaptchaWait)
	for time.Now().Before(deadline) {
		if s.waitContextReady(page, 3*time.Second) {
			log.Println("人工处理完成，继续提取详情")
			return nil
		}
	}
	return &blockError{"1688 详情页触发风控/登录拦截，等待人工处理超时（" + s.cfg.CaptchaWait.String() + "），请在浏览器窗口完成验证后重试"}
}

// descriptionImgPattern 详情描述中的图片地址。
// 描述 URL 返回 JS 变量（var offer_details={"content":"<img src=\"...\">"}），
// 引号是 JSON 转义的 \"，所以匹配时要兼容反斜杠。
var descriptionImgPattern = regexp.MustCompile(`(?:src|data-src)=\\?["']?(https?://[^"'\s>\\]+)`)

// fetchDescriptionImages 拉取详情描述 HTML 并提取图片 URL（最多 limit 张）。
// 失败时返回 nil（详情图为可选字段，不影响主流程）。
func fetchDescriptionImages(descURL string, limit int) []string {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(descURL)
	if err != nil {
		log.Printf("拉取详情描述失败: %v", err)
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, u := range descriptionImgPattern.FindAllStringSubmatch(string(body), -1) {
		uu := u[1]
		if seen[uu] {
			continue
		}
		seen[uu] = true
		out = append(out, uu)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// domGoodsImgJS 从 DOM 收集商品主图（context 缺失时的兜底）。
const domGoodsImgJS = `() => {
	const seen = {};
	const out = [];
	[...document.querySelectorAll('img')].forEach(i => {
		const u = i.src || i.getAttribute('data-src') || '';
		if (/cbu01\.alicdn\.com\/img\/ibank\//.test(u) && !seen[u]) {
			seen[u] = 1;
			out.push(u);
		}
	});
	return JSON.stringify(out.slice(0, 15));
}`

// collectDetailImagesFromDOM 从页面 DOM 收集商品图（前 15 张）。
func collectDetailImagesFromDOM(page *rod.Page) []string {
	res, err := page.Eval(domGoodsImgJS)
	if err != nil {
		return nil
	}
	var imgs []string
	if err := json.Unmarshal([]byte(res.Value.Str()), &imgs); err != nil {
		return nil
	}
	return imgs
}

// formatDetailNumber 把数字格式化为易读文本（1,234,567）。
func formatDetailNumber(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteString(",")
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

var _ = os.Stdout // 保留 os 引用（调试扩展用）
