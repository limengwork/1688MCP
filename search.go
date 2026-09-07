package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// SearchOptions MCP 工具 search_goods 的入参。
type SearchOptions struct {
	Keyword          string  `json:"keyword"`
	SortType         string  `json:"sort_type,omitempty"` // default | sales | price_asc
	PriceMin         float64 `json:"price_min,omitempty"`
	PriceMax         float64 `json:"price_max,omitempty"`
	OnlyDropshipping bool    `json:"only_dropshipping,omitempty"`
	Page             int     `json:"page,omitempty"`
}

// SearchResult 单个商品的搜索结果。
type SearchResult struct {
	Title    string   `json:"title"`
	Price    float64  `json:"price"`
	Sales    int      `json:"sales"`
	URL      string   `json:"url"`
	ImageURL string   `json:"image_url"`
	ShopName string   `json:"shop_name"`
	Tags     []string `json:"tags,omitempty"`
}

// SearchResponse 搜索响应。
type SearchResponse struct {
	Items      []SearchResult `json:"items"`
	TotalCount int            `json:"total_count"`
}

// SearchService 封装 1688 搜索逻辑。
type SearchService struct {
	browser *BrowserManager
	cfg     *Config
}

func NewSearchService(bm *BrowserManager, cfg *Config) *SearchService {
	return &SearchService{browser: bm, cfg: cfg}
}

const (
	// pollInterval 轮询结果出现的间隔
	pollInterval = 500 * time.Millisecond
)

// errNoResult 页面明确提示无搜索结果，属于正常情况而非异常
var errNoResult = errors.New("no result")

// blockError 页面被拦截（风控验证/登录页），hint 是给用户的可操作提示。
type blockError struct{ hint string }

func (e *blockError) Error() string { return e.hint }

// itemSelectors 搜索结果项的候选选择器。
// 1688 新版结果卡片带稳定属性 data-offer-grid-cell；类名是 CSS Modules 构建哈希
// （如 offerList--FsydnV_n），每次构建都会变，只能取哈希前缀作兜底。
var itemSelectors = []string{
	"div[data-offer-grid-cell='true']", // 2024+ 新版：结果卡片稳定属性
	"div[class*='gridCell']",           // 新版类名前缀兜底
	".sm-offer-item",                   // 旧版列表
	"div.space-offer-card-box",         // 过渡版卡片
	"div.outer-offer-item",             // 过渡版外层项
}

// Search 执行一次 1688 商品搜索。
// 导航/等待类超时会自动重试一次（README：超时、重试机制）；
// 风控/登录拦截、空结果、页面结构变化不属于超时，不重试。
func (s *SearchService) Search(keyword string, options SearchOptions) (*SearchResponse, error) {
	options.Keyword = strings.TrimSpace(keyword)
	if options.Keyword == "" {
		return nil, fmt.Errorf("keyword 不能为空")
	}
	if err := normalizeOptions(&options); err != nil {
		return nil, err
	}

	var resp *SearchResponse
	var err error
	for attempt := 1; attempt <= 2; attempt++ {
		resp, err = s.runSearch(options)
		if err == nil || !isTimeoutError(err) {
			break
		}
		if attempt == 1 {
			log.Printf("搜索超时: %v，自动重试一次", err)
		} else {
			log.Printf("搜索重试后仍超时: %v", err)
		}
	}
	if err != nil {
		if errors.Is(err, errNoResult) {
			return &SearchResponse{Items: []SearchResult{}}, nil
		}
		return nil, err
	}
	return resp, nil
}

// runSearch 执行单次「打开搜索页 → 等待结果 → 提取」流程。
func (s *SearchService) runSearch(options SearchOptions) (*SearchResponse, error) {
	page, err := s.browser.NewPage(buildSearchURL(options))
	if err != nil {
		return nil, fmt.Errorf("打开搜索页失败: %w", err)
	}
	defer page.Close()
	page = page.Timeout(s.cfg.PageTimeout)

	// 导航后随机停顿，模拟真人浏览节奏（反爬）
	randomSleep(1, 3)

	if err := s.waitResults(page); err != nil {
		if errors.Is(err, errNoResult) {
			return nil, err
		}
		if hint := detectBlock(page); hint != "" {
			// 拦截页（风控滑块/登录）：先给人工处理机会，通过后继续本页流程
			if werr := s.waitHumanSolve(page); werr != nil {
				return nil, werr
			}
		} else {
			return nil, err
		}
	}

	// 页面控件筛选：勾选「一件代发」、填价格区间。URL 参数先行（buildSearchURL），
	// 控件若已因 URL 参数呈选中/已填状态则自动跳过；筛选触发刷新后等结果重新渲染。
	if err := s.applyPageFilters(page, options); err != nil {
		return nil, err
	}

	// 滚动页面触发图片懒加载
	lazyLoad(page)

	items := s.extractSearchResults(page)
	if len(items) == 0 {
		if hint := detectBlock(page); hint != "" {
			return nil, errors.New(hint)
		}
		return nil, fmt.Errorf("未提取到搜索结果，可能 1688 页面结构已变化，请检查 search.go 中的 itemSelectors 与字段选择器")
	}
	return &SearchResponse{
		Items:      items,
		TotalCount: len(items),
	}, nil
}

// dropshippingStateJS 检测结果页「一件代发」筛选的当前状态（只读）。
const dropshippingStateJS = `() => {
	let textEl = null;
	const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
	while (walker.nextNode()) {
		if ((walker.currentNode.textContent || '').indexOf('一件代发') >= 0) {
			textEl = walker.currentNode.parentElement;
			break;
		}
	}
	if (!textEl) return JSON.stringify({ found: false });
	let container = textEl;
	for (let i = 0; i < 6 && container.parentElement; i++) {
		if (/filtItem/i.test((container.className || '').toString())) break;
		container = container.parentElement;
	}
	const input = container.tagName === 'INPUT' && container.type === 'checkbox'
		? container
		: container.querySelector('input[type=checkbox]');
	const allCls = (container.className || '').toString() + ' ' + (textEl.className || '').toString();
	const already = (input && input.checked) || /checked|selected|active/i.test(allCls);
	return JSON.stringify({ found: true, already: already });
}`

// dropshippingFinderJS 定位未勾选时需点击的元素（input 或筛选 item 容器）。
const dropshippingFinderJS = `() => {
	let textEl = null;
	const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
	while (walker.nextNode()) {
		if ((walker.currentNode.textContent || '').indexOf('一件代发') >= 0) {
			textEl = walker.currentNode.parentElement;
			break;
		}
	}
	if (!textEl) return null;
	let container = textEl;
	for (let i = 0; i < 6 && container.parentElement; i++) {
		if (/filtItem/i.test((container.className || '').toString())) break;
		container = container.parentElement;
	}
	const input = container.tagName === 'INPUT' && container.type === 'checkbox'
		? container
		: container.querySelector('input[type=checkbox]');
	return input || container;
}`

// applyPageFilters 在结果页上通过页面控件应用排序与筛选：点「销量」排序、
// 勾选「一件代发」、填价格区间。每个动作都会触发结果区刷新，操作后等待
// URL 变化/结果快照变化确认生效；未检测到变化时报错，不静默返回未生效数据。
func (s *SearchService) applyPageFilters(page *rod.Page, options SearchOptions) error {
	if options.SortType == "sales" {
		if err := s.applySalesSort(page); err != nil {
			return fmt.Errorf("销量排序失败: %w", err)
		}
	}
	if options.OnlyDropshipping {
		if err := s.checkDropshipping(page); err != nil {
			return fmt.Errorf("一件代发筛选失败: %w", err)
		}
	}
	if options.PriceMin > 0 || options.PriceMax > 0 {
		if err := s.fillPriceRange(page, options.PriceMin, options.PriceMax); err != nil {
			return fmt.Errorf("价格区间筛选失败: %w", err)
		}
	}
	return nil
}

// salesSortFinderJS 定位排序栏里的「销量」选项（链接/标签均可），返回元素供受信点击。
// 类名是构建哈希，按「自身文本恰好为 销量」的可见元素查找，优先取排序栏内的，
// 多个命中时取面积最小的（最里层、最像可点标签的那个）。
const salesSortFinderJS = `() => {
	const cands = [...document.querySelectorAll('a,button,span,div,li')].filter(el => {
		const t = (el.innerText || '').trim();
		return t === '销量' && (el.offsetWidth || 0) > 0 && (el.offsetHeight || 0) > 0;
	});
	if (!cands.length) return null;
	const inBar = cands.filter(el => {
		let p = el;
		for (let i = 0; i < 6 && p; i++) {
			if (/sort/i.test((p.className || '').toString())) return true;
			p = p.parentElement;
		}
		return false;
	});
	const pool = inBar.length ? inBar : cands;
	pool.sort((a, b) => a.offsetWidth * a.offsetHeight - b.offsetWidth * b.offsetHeight);
	return pool[0];
}`

// applySalesSort 点击页面排序栏的「销量」选项触发按销量排序。
// URL 的 sortType=booked 参数实测不可靠（结果近似价格降序），必须点页面控件。
func (s *SearchService) applySalesSort(page *rod.Page) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	el, err := page.Context(ctx).ElementByJS(rod.Eval(salesSortFinderJS))
	if err != nil || el == nil {
		return errors.New("页面上未找到「销量」排序选项（页面结构可能已变化）")
	}
	beforeURL := currentURL(page)
	beforeSnap := resultSnapshot(page)
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击「销量」排序失败: %w", err)
	}
	log.Println("已受信点击「销量」排序，等待结果刷新...")
	randomSleep(1, 2)
	return s.waitFilterApplied(page, beforeURL, beforeSnap, "sortType")
}

// checkDropshipping 勾选「一件代发」筛选并用受信鼠标事件等待筛选生效。
func (s *SearchService) checkDropshipping(page *rod.Page) error {
	res, err := page.Eval(dropshippingStateJS)
	if err != nil {
		return fmt.Errorf("检测筛选状态失败: %w", err)
	}
	var state struct {
		Found   bool `json:"found"`
		Already bool `json:"already"`
	}
	if err := json.Unmarshal([]byte(res.Value.Str()), &state); err != nil {
		return fmt.Errorf("解析筛选状态失败: %w", err)
	}
	if !state.Found {
		return errors.New("页面上未找到「一件代发」筛选控件（页面结构可能已变化）")
	}
	if state.Already {
		log.Println("一件代发：已处于勾选状态（URL 参数生效），跳过点击")
		return nil
	}

	// 1688 前端忽略非受信事件（JS click），用 rod 模拟真实鼠标点击
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	el, err := page.Context(ctx).ElementByJS(rod.Eval(dropshippingFinderJS))
	if err != nil || el == nil {
		return fmt.Errorf("定位「一件代发」控件失败: %w", err)
	}
	beforeURL := currentURL(page)
	beforeSnap := resultSnapshot(page)
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击「一件代发」失败: %w", err)
	}
	log.Println("已受信点击勾选「一件代发」，等待筛选生效...")
	randomSleep(1, 2)
	// 点击复选框后 1688 通过 filtOfferTags 参数记录筛选态（如 98306=一件代发）
	return s.waitFilterApplied(page, beforeURL, beforeSnap, "filtOfferTags")
}

// fillPriceRange 在「¥最低价」「¥最高价」输入框填入价格区间并回车触发筛选。
func (s *SearchService) fillPriceRange(page *rod.Page, priceMin, priceMax float64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	p := page.Context(ctx)

	minEl, err := p.Element("input[placeholder*='最低价']")
	if err != nil {
		return errors.New("页面上未找到「最低价」输入框（页面结构可能已变化）")
	}
	maxEl, err := p.Element("input[placeholder*='最高价']")
	if err != nil {
		return errors.New("页面上未找到「最高价」输入框（页面结构可能已变化）")
	}

	// 输入框已有值说明 URL 参数已被服务端接受并回显，跳过填入
	if v := inputValue(minEl); strings.TrimSpace(v) != "" && strings.TrimSpace(inputValue(maxEl)) != "" {
		log.Printf("价格区间输入框已有值（%s），URL 参数生效，跳过填入", v)
		return nil
	}

	randomSleep(0.3, 0.8)
	if err := minEl.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击最低价输入框失败: %w", err)
	}
	_ = minEl.SelectAllText()
	if priceMin > 0 {
		if err := minEl.Input(strconv.FormatFloat(priceMin, 'f', -1, 64)); err != nil {
			return fmt.Errorf("输入最低价失败: %w", err)
		}
	}
	randomSleep(0.3, 0.8)
	if err := maxEl.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击最高价输入框失败: %w", err)
	}
	_ = maxEl.SelectAllText()
	if priceMax > 0 {
		if err := maxEl.Input(strconv.FormatFloat(priceMax, 'f', -1, 64)); err != nil {
			return fmt.Errorf("输入最高价失败: %w", err)
		}
	}
	randomSleep(0.3, 0.8)
	// 1688 价格输入完成后会在输入框旁生成「确认」按钮，点击它才触发筛选；Enter 仅作兜底
	beforeURL := currentURL(page)
	beforeSnap := resultSnapshot(page)
	clicked, err := clickPriceConfirm(page)
	if err != nil {
		return err
	}
	if !clicked {
		if err := maxEl.Type(input.Enter); err != nil {
			return fmt.Errorf("回车触发筛选失败: %w", err)
		}
	}
	log.Printf("已填入价格区间 [%.2f, %.2f]（确认按钮=%v），等待筛选生效...", priceMin, priceMax, clicked)
	randomSleep(1, 2)
	return s.waitFilterApplied(page, beforeURL, beforeSnap, "price_start", "price_end")
}

// priceConfirmFinderJS 在价格输入框附近查找可见的「确认/确定」按钮，返回元素供 rod 受信点击。
// 1688 类名是构建哈希（CSS Modules），确认按钮未必是 button/btn 类名，
// 因此从价格输入框向上逐层找容器，在容器内找文本恰好为「确定/确认」的
// 可见元素（不限标签），多个命中时取面积最小的（最里层、最像按钮的那个）。
const priceConfirmFinderJS = `() => {
	const anchors = [
		[...document.querySelectorAll('input')].find(i => /最低价/.test(i.placeholder || '')),
		[...document.querySelectorAll('input')].find(i => /最高价/.test(i.placeholder || '')),
	].filter(Boolean);
	for (const anchor of anchors) {
		let container = anchor;
		for (let i = 0; i < 8 && container.parentElement; i++) {
			container = container.parentElement;
			const cands = [...container.querySelectorAll('*')].filter(el => {
				const t = (el.innerText || '').trim();
				return /^(确定|确认)$/.test(t) && (el.offsetWidth || 0) > 0 && (el.offsetHeight || 0) > 0;
			});
			if (cands.length) {
				cands.sort((a, b) => a.offsetWidth * a.offsetHeight - b.offsetWidth * b.offsetHeight);
				return cands[0];
			}
		}
	}
	return null;
}`

// clickPriceConfirm 用受信鼠标事件（CDP）点击价格确认按钮。
// JS 的 el.click() 是非受信事件（trusted=false），1688 前端框架会忽略，
// 必须走 rod 的 Element.Click（模拟真实鼠标）才能触发筛选。
func clickPriceConfirm(page *rod.Page) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	btn, err := page.Context(ctx).ElementByJS(rod.Eval(priceConfirmFinderJS))
	if err != nil || btn == nil {
		log.Printf("未找到价格确认按钮（%v），回车兜底", err)
		return false, nil
	}
	if err := btn.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return false, fmt.Errorf("点击确认按钮失败: %w", err)
	}
	log.Println("已受信点击价格确认按钮")
	return true, nil
}

// inputValue 读取输入框当前 value。
func inputValue(el *rod.Element) string {
	v, err := el.Property("value")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v.Str())
}

// currentURL 读取当前页面地址；失败时返回空串。
func currentURL(page *rod.Page) string {
	res, err := page.Eval("() => location.href")
	if err != nil {
		return ""
	}
	return res.Value.Str()
}

// resultSnapshot 取前 3 张结果卡片的首行文本拼接，作为结果区是否刷新的对比快照。
const snapshotJS = `() => {
	const cells = [...document.querySelectorAll("div[data-offer-grid-cell='true']")];
	return cells.slice(0, 3).map(c => ((c.innerText || '').split('\n')[0] || '').trim()).join('|');
}`

func resultSnapshot(page *rod.Page) string {
	res, err := page.Eval(snapshotJS)
	if err != nil {
		return ""
	}
	return res.Value.Str()
}

// waitFilterApplied 等待筛选真正触发。判定信号（满足其一，均对比操作前状态）：
//  1. URL 出现预期参数（如 filtOfferTags、price_start）；
//  2. URL 相比操作前发生了变化；
//  3. 结果区卡片快照发生变化（结果真正刷新的硬证据）。
//
// 命中后再等结果刷新完成（waitResultsRefresh），否则会提取到筛选前的旧结果。
func (s *SearchService) waitFilterApplied(page *rod.Page, beforeURL, beforeSnap string, urlParams ...string) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		triggered := ""
		if href := currentURL(page); href != "" && href != beforeURL {
			for _, p := range urlParams {
				if strings.Contains(href, p+"=") {
					triggered = "URL 参数 " + p
					break
				}
			}
			if triggered == "" {
				triggered = "URL 变化: " + href
			}
		}
		if triggered == "" && beforeSnap != "" {
			if after := resultSnapshot(page); after != "" && after != beforeSnap {
				triggered = "结果区刷新"
			}
		}
		if triggered != "" {
			log.Printf("筛选已触发（%s），等待结果刷新...", triggered)
			return s.waitResultsRefresh(page, beforeSnap)
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("筛选后未检测到页面刷新（params=%v），筛选可能未生效", urlParams)
}

// waitResultsRefresh 等待筛选触发的结果刷新完成。
// URL 变了不代表新结果已渲染——AJAX 刷新期间旧卡片仍留在页面上，
// 所以先等快照变化（或超时兜底），再确认结果可提取；0 结果（空态）是合法状态。
func (s *SearchService) waitResultsRefresh(page *rod.Page, beforeSnap string) error {
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		after := resultSnapshot(page)
		if beforeSnap == "" || (after != "" && after != beforeSnap) {
			break
		}
		time.Sleep(pollInterval)
	}
	err := s.waitResults(page)
	if err == nil || errors.Is(err, errNoResult) {
		return nil
	}
	return err
}

// isTimeoutError 判断是否为超时类错误。rod 导航超时会包裹 context deadline
// exceeded；waitResults 的错误消息含「超时」。拦截类错误（blockError）
// 不属于超时，即使消息里带「超时」字样也不重试。
func isTimeoutError(err error) bool {
	var be *blockError
	if errors.As(err, &be) {
		return false
	}
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "超时")
}

// waitHumanSolve 命中风控/登录拦截后，等待人工在浏览器窗口处理。
// 期间以 waitResults 窗口轮询结果/空态，最长等待 cfg.CaptchaWait；
// 通过后返回 nil（调用方继续本页提取），超时返回 blockError。
func (s *SearchService) waitHumanSolve(page *rod.Page) error {
	log.Printf("命中风控/登录拦截，等待人工处理（最长 %s），完成后自动继续...", s.cfg.CaptchaWait)
	deadline := time.Now().Add(s.cfg.CaptchaWait)
	for {
		err := s.waitResults(page)
		if err == nil {
			log.Println("人工处理完成，继续提取搜索结果")
			return nil
		}
		if errors.Is(err, errNoResult) {
			return err
		}
		if !time.Now().Before(deadline) {
			return &blockError{"1688 触发了风控/登录拦截，等待人工处理超时（" + s.cfg.CaptchaWait.String() + "），请在浏览器窗口完成验证后重试"}
		}
	}
}

// normalizeOptions 校验并归一化搜索参数。
func normalizeOptions(options *SearchOptions) error {
	switch options.SortType {
	case "", "default", "sales", "price_asc":
	default:
		return fmt.Errorf("sort_type 仅支持 default / sales / price_asc，当前为 %q", options.SortType)
	}
	if options.Page < 1 {
		options.Page = 1
	}
	if options.PriceMin > 0 && options.PriceMax > 0 && options.PriceMin > options.PriceMax {
		return fmt.Errorf("price_min（%v）不能大于 price_max（%v）", options.PriceMin, options.PriceMax)
	}
	return nil
}

// buildSearchURL 构造 1688 selloffer 搜索页地址。
// 注意：1688 按 GBK 解码查询参数，中文值必须先转 GBK 再百分号编码，
// 否则关键词到服务端是乱码，搜不出任何商品。
// 实测 1688 服务端不认 price_start/price_end/offerFilter 等 URL 参数；
// 销量排序 sortType=booked 也不可靠（结果近似价格降序），这些一律走页面控件
// 交互（applyPageFilters），这里只放默认综合排序（不带 sortType）、
// price_asc 排序（sortType=price）与翻页参数。
func buildSearchURL(options SearchOptions) string {
	values := url.Values{}
	values.Set("keywords", toGBK(options.Keyword))

	// 销量排序走页面「销量」按钮（applySalesSort）；价格升序 URL 参数有效
	if options.SortType == "price_asc" {
		values.Set("sortType", "price")
	}

	if options.Page > 1 {
		values.Set("beginPage", strconv.Itoa(options.Page))
	}

	return "https://s.1688.com/selloffer/offer_search.htm?" + values.Encode()
}

// toGBK 把字符串转成 GBK 字节串。url.Values.Encode 会对字节逐个百分号编码，
// 恰好得到 1688 期望的 GBK 编码参数。个别 GBK 外的字符退回原 UTF-8。
func toGBK(s string) string {
	b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	if err != nil {
		return s
	}
	return string(b)
}

// waitResults 轮询等待搜索结果出现，或页面明确提示无结果。
func (s *SearchService) waitResults(page *rod.Page) error {
	deadline := time.Now().Add(s.cfg.ResultWait)
	for time.Now().Before(deadline) {
		for _, sel := range itemSelectors {
			if els, err := page.Elements(sel); err == nil && len(els) > 0 {
				return nil
			}
		}
		if empty, _ := pageHasAnyText(page, "空空如也", "没有找到", "无搜索结果", "没有相关的"); empty {
			return errNoResult
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("等待搜索结果超时（%s）", s.cfg.ResultWait)
}

// pageHasAnyText 判断页面文本里是否包含其中任意一个片段。
func pageHasAnyText(page *rod.Page, fragments ...string) (bool, error) {
	res, err := page.Eval("() => document.body ? document.body.innerText : ''")
	if err != nil {
		return false, err
	}
	text := res.Value.Str()
	for _, f := range fragments {
		if strings.Contains(text, f) {
			return true, nil
		}
	}
	return false, nil
}

// detectBlock 检查是否被 1688 拦截（跳登录页/风控验证），命中时返回给用户的提示。
func detectBlock(page *rod.Page) string {
	res, err := page.Eval(`() => location.href + ' ||| ' + document.title`)
	if err != nil {
		return ""
	}
	text := res.Value.Str()

	switch {
	case strings.Contains(text, "login.1688.com"), strings.Contains(text, "login.taobao.com"):
		return "1688 跳转到了登录页，请先在有头模式下启动服务并手动登录一次（登录态保存在 user_data 目录），再重试"
	case strings.Contains(text, "punish"), strings.Contains(text, "captcha"), strings.Contains(text, "验证"):
		return "1688 触发了风控验证，请在浏览器窗口中手动完成验证后重试"
	}
	return ""
}

// lazyLoad 分段向下滚动，触发商品图片懒加载，再回到顶部。
func lazyLoad(page *rod.Page) {
	for i := 0; i < 4; i++ {
		_, _ = page.Eval("() => window.scrollBy(0, 1000)")
		randomSleep(0.3, 0.8)
	}
	_, _ = page.Eval("() => window.scrollTo(0, 0)")
	randomSleep(0.5, 1)
}

// extractItemsJS 提取搜索结果卡片。
// 1688 新版卡片的类名是构建哈希，不可依赖；稳定锚点：
//   - 卡片容器属性 data-offer-grid-cell、商品ID属性 data-offer-expose-id
//   - 可见文本行规律：首行是标题，"¥" 行与后续数字行（含 ".5" 小数行）拼出价格，
//     标签区（"｜" 分隔的类目/特征标签）在标题与价格行之间，末行通常是公司名
//   - 部分卡片可见行里没有公司名（如只有"回头率"收尾），此时回退到
//     hoverPanel 内的 descText（隐藏悬浮面板的公司名，用 textContent 取）
const extractItemsJS = `() => {
	const cells = [...document.querySelectorAll("div[data-offer-grid-cell='true']")];
	const numRe = /^[0-9.,]+$/;
	const noiseRe = /(包邮|运费|无理由|先采后付|回头率|元宝|限时|新人价|找相似|已售|月销|成交|退货运费|全网|\d+件)/;
	const salesRe = /(已售|月销|成交|全网)|件\s*$/;
	const shopRe = /(有限公司|公司|商行|贸易|实业|经营部|经销部|批发部|厂|百货|商店|店铺|商行)/;
	const items = [];
	cells.forEach(cell => {
		const lines = (cell.innerText || '').split('\n').map(s => s.trim()).filter(Boolean);
		if (!lines.length) return;
		const a = cell.querySelector("a[href*='offerId']") || cell.querySelector("a[href]");
		const img = cell.querySelector("img[class*='mainImg']") || cell.querySelector("img");

		// 店铺名：优先可见行中的公司名（避免把"仿古"等属性行误当店名）
		let shopName = '';
		for (const ln of lines) {
			if (shopRe.test(ln) && !noiseRe.test(ln)) { shopName = ln; break; }
		}
		if (!shopName) {
			const hp = cell.querySelector("[class*='hoverPanel'] [class*='descText']");
			if (hp) shopName = (hp.textContent || '').trim();
		}

		// 销量行
		let salesText = '';
		for (const ln of lines) {
			if (salesRe.test(ln)) { salesText = ln; break; }
		}

		// 价格：¥ 行 + 后续连续数字行（".5" 之类小数拆分行可被 numRe 覆盖拼接）
		let priceText = '';
		let priceLineIdx = -1;
		for (let i = 0; i < lines.length; i++) {
			if (lines[i] === '¥' || (lines[i].charAt(0) === '¥' && lines[i].length > 1)) {
				let p = lines[i] === '¥' ? '' : lines[i].slice(1);
				let j = i + 1;
				for (; j < lines.length && numRe.test(lines[j]); j++) p += lines[j];
				if (p) { priceText = p; priceLineIdx = i; break; }
			}
		}

		// 标签区：标题与价格行之间的非"｜"文本行（类目/风格/工艺/优惠等特征）
		// 用于调用方过滤弱相关商品（booked 排序会混入高销量但类目不符的商品）
		const tags = [];
		const stopIdx = priceLineIdx > 0 ? priceLineIdx : lines.length;
		for (let i = 1; i < stopIdx && tags.length < 6; i++) {
			const ln = lines[i];
			if (ln === '｜' || ln === '|' || ln === '') continue;
			if (noiseRe.test(ln) || salesRe.test(ln)) continue;
			if (numRe.test(ln)) continue;
			tags.push(ln);
		}

		items.push({
			offerId: cell.getAttribute('data-offer-expose-id') || '',
			title: lines[0] || '',
			priceText: priceText,
			salesText: salesText,
			shopName: shopName,
			tags: tags,
			imageUrl: img ? (img.getAttribute('data-src') || img.getAttribute('src') || '') : '',
			detailUrl: a ? a.href : ''
		});
	});
	return JSON.stringify(items);
}`

// rawItem extractItemsJS 返回的原始卡片数据
type rawItem struct {
	OfferID   string   `json:"offerId"`
	Title     string   `json:"title"`
	PriceText string   `json:"priceText"`
	SalesText string   `json:"salesText"`
	ShopName  string   `json:"shopName"`
	Tags      []string `json:"tags"`
	ImageURL  string   `json:"imageUrl"`
	DetailURL string   `json:"detailUrl"`
}

// extractSearchResults 从搜索页 DOM 提取商品列表。
func (s *SearchService) extractSearchResults(page *rod.Page) []SearchResult {
	res, err := page.Eval(extractItemsJS)
	if err != nil {
		return nil
	}
	var raws []rawItem
	if err := json.Unmarshal([]byte(res.Value.Str()), &raws); err != nil {
		return nil
	}

	results := make([]SearchResult, 0, len(raws))
	seen := make(map[string]bool, len(raws))
	for _, raw := range raws {
		title := cleanText(raw.Title)
		if title == "" {
			continue
		}
		url := strings.TrimSpace(raw.DetailURL)
		if url == "" && raw.OfferID != "" {
			url = "https://detail.1688.com/offer/" + raw.OfferID + ".html"
		}
		if url != "" {
			if seen[url] {
				continue
			}
			seen[url] = true
		}
		results = append(results, SearchResult{
			Title:    title,
			Price:    parsePriceText(raw.PriceText),
			Sales:    parseSalesText(raw.SalesText),
			ShopName: cleanText(raw.ShopName),
			Tags:     raw.Tags,
			ImageURL: cleanImageURL(raw.ImageURL),
			URL:      url,
		})
	}
	return results
}

var (
	numberRe = regexp.MustCompile(`\d+(?:\.\d+)?`)
	// 已售600+件 / 全网3万+件 / 成交3000笔 / 月销500+ 等
	salesWithKeywordRe = regexp.MustCompile(`(?:已售|成交|月销|销量|全网)[^\d]{0,6}(\d+(?:\.\d+)?)\s*(万|w|W)?`)
	// 纯数字销量文本：1万+ / 3000+ / 600+件
	salesPlainRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)(万|w|W)?\+?(?:件)?$`)
)

// parsePriceText 从价格元素文本解析价格，支持 "¥1.50" "1.50-2.50"（区间取下限）。
func parsePriceText(text string) float64 {
	text = strings.NewReplacer("¥", "", "￥", "", ",", "", " ", "").Replace(text)
	m := numberRe.FindString(text)
	if m == "" {
		return 0
	}
	v, err := strconv.ParseFloat(m, 64)
	if err != nil {
		return 0
	}
	return v
}

// parseSalesText 从销量文本解析月销量，"万/w" 单位换算为具体数值。
func parseSalesText(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	if m := salesPlainRe.FindStringSubmatch(text); m != nil {
		return salesToUnit(m[1], m[2])
	}
	if m := salesWithKeywordRe.FindStringSubmatch(text); m != nil {
		return salesToUnit(m[1], m[2])
	}
	return 0
}

func salesToUnit(num, unit string) int {
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	if unit != "" {
		v *= 10000
	}
	return int(v)
}

// cleanImageURL 清理主图地址，跳过 data: 占位图。
func cleanImageURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" || strings.HasPrefix(u, "data:") {
		return ""
	}
	return absURL(u)
}

// cleanText 压缩空白字符。
func cleanText(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// absURL 相对链接补全为绝对链接。
func absURL(href string) string {
	href = strings.TrimSpace(href)
	switch {
	case href == "":
		return ""
	case strings.HasPrefix(href, "//"):
		return "https:" + href
	case strings.HasPrefix(href, "/"):
		return "https://s.1688.com" + href
	default:
		return href
	}
}
