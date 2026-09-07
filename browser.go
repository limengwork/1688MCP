package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// BrowserManager 管理 rod 浏览器实例的生命周期。
type BrowserManager struct {
	browser *rod.Browser
}

// browserConfig 浏览器启动配置。
type browserConfig struct {
	// headless 是否无头模式。默认 false（有头），便于首次登录和调试。
	headless bool
	// userDataDir 用户数据目录，持久化 cookie/登录态，避免每次都触发登录和验证。
	userDataDir string
}

// BrowserOption 浏览器配置选项。
type BrowserOption func(*browserConfig)

// WithHeadless 设置是否无头模式。
func WithHeadless(headless bool) BrowserOption {
	return func(c *browserConfig) {
		c.headless = headless
	}
}

// WithUserDataDir 设置用户数据目录（登录态保存在这里）。
func WithUserDataDir(dir string) BrowserOption {
	return func(c *browserConfig) {
		c.userDataDir = dir
	}
}

func defaultBrowserConfig() browserConfig {
	return browserConfig{
		headless:    false,
		userDataDir: filepath.Join(".", "user_data"),
	}
}

// NewBrowserManager 启动并连接浏览器。
func NewBrowserManager(opts ...BrowserOption) (*BrowserManager, error) {
	cfg := defaultBrowserConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	// 登录态目录转绝对路径：保证进程占用检测不受启动目录影响，也让日志路径明确
	absDir, absErr := filepath.Abs(cfg.userDataDir)
	if absErr != nil {
		absDir = cfg.userDataDir
	}
	cfg.userDataDir = absDir

	// 提前建好目录，浏览器写 profile 时不会因为目录不存在而报错
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建用户数据目录失败: %w", err)
	}

	// 启动前检测：已有 Chrome 实例占用该登录态目录时，新实例会拿不到 profile 锁、
	// 启动即退出（rod 报 "Failed to get the debug url: exit status 1"）。提前给出可操作提示。
	if pids := chromePIDsUsingProfile(absDir); len(pids) > 0 {
		return nil, profileInUseError(absDir, pids)
	}

	l := launcher.New()

	// 优先用系统已安装的 Chrome/Edge：
	// 免去 rod 首次运行自动下载 100+MB 的托管 Chromium，真实浏览器也更不易被风控。
	// 找不到已安装浏览器时，回退 rod 托管浏览器（自动下载）。
	if binPath, ok := launcher.LookPath(); ok {
		l = l.Bin(binPath)
	}

	l = l.
		Headless(cfg.headless).
		UserDataDir(cfg.userDataDir).
		Set("no-sandbox").
		Set("disable-gpu").
		Set("disable-infobars").
		Set("disable-dev-shm-usage").
		Set("disable-blink-features", "AutomationControlled").
		Set("lang", "zh-CN").
		Set("window-size", "1440,900")

	controlURL, err := l.Launch()
	if err != nil {
		// 启动失败后再检测一次：可能是旧实例恰好在此时占用了目录
		if pids := chromePIDsUsingProfile(absDir); len(pids) > 0 {
			return nil, fmt.Errorf("启动浏览器失败: %w", profileInUseError(absDir, pids))
		}
		return nil, fmt.Errorf("启动浏览器失败: %w（请确认本机已安装 Chrome/Edge；若反复失败可尝试删除登录态目录 %s 后重启）", err, absDir)
	}

	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("连接浏览器失败: %w", err)
	}

	return &BrowserManager{browser: browser}, nil
}

// NewPage 新开一个页面并导航到 url。
func (bm *BrowserManager) NewPage(url string) (*rod.Page, error) {
	page, err := bm.browser.Page(proto.TargetCreateTarget{URL: url})
	if err != nil {
		return nil, err
	}

	// 隐藏 webdriver 标记，降低被识别为自动化浏览器的概率
	_, _ = page.Eval(`() => {
		Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
	}`)

	return page, nil
}

// Close 关闭浏览器。
func (bm *BrowserManager) Close() {
	if bm.browser != nil {
		_ = bm.browser.Close()
	}
}

// randomSleep 随机停顿 [minSec, maxSec] 秒，模拟真人操作节奏（反爬）。
func randomSleep(minSec, maxSec float64) {
	d := minSec + rand.Float64()*(maxSec-minSec)
	time.Sleep(time.Duration(d * float64(time.Second)))
}

// chromePIDsUsingProfile 返回命令行中引用了指定用户数据目录的 chrome.exe 进程 PID。
// 这些进程持有 profile 锁，会导致新 Chrome 实例启动即退出。
// 仅 Windows 生效；查询失败返回空切片（不阻断主流程）。
func chromePIDsUsingProfile(absDir string) []int {
	if runtime.GOOS != "windows" {
		return nil
	}
	// PowerShell 单引号字符串里，单引号需双写转义
	target := strings.ReplaceAll(strings.ToLower(absDir), "'", "''")
	script := "$ErrorActionPreference='SilentlyContinue';" +
		"Get-CimInstance Win32_Process -Filter \"Name='chrome.exe'\" | " +
		"Where-Object { $_.CommandLine -and $_.CommandLine.ToLower().Contains('" + target + "') } | " +
		"ForEach-Object { $_.ProcessId }"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// profileInUseError 构造「登录态目录被占用」的可操作错误。
func profileInUseError(absDir string, pids []int) error {
	return fmt.Errorf("登录态目录 %s 已被另一个 Chrome 实例占用（PID: %v）；"+
		"同一时间只能运行一个 mcp-1688 服务，请先关闭旧的 mcp-1688 服务"+
		"（任务管理器结束 mcp-1688.exe，或结束这些 chrome.exe 进程）后再启动", absDir, pids)
}

// getOrOpenHomePage 返回已打开的 1688 首页标签页（优先 www 首页，其次任意 1688 页）；
// 一个都没有则新开首页并等加载完成。登录状态检测基于首页的 URL/Cookie/DOM。
func (bm *BrowserManager) getOrOpenHomePage() (*rod.Page, error) {
	pages, err := bm.browser.Pages()
	if err != nil {
		return nil, err
	}
	var any1688 *rod.Page
	for _, p := range pages {
		info, err := p.Info()
		if err != nil {
			continue
		}
		u := info.URL
		if !strings.Contains(u, "1688.com") {
			continue
		}
		if strings.Contains(u, "//www.1688.com") || strings.Contains(u, "//1688.com") {
			return p, nil
		}
		if any1688 == nil {
			any1688 = p
		}
	}
	if any1688 != nil {
		return any1688, nil
	}
	page, err := bm.NewPage("https://www.1688.com")
	if err != nil {
		return nil, err
	}
	_ = page.Timeout(20 * time.Second).WaitLoad()
	return page, nil
}

// LoginStatus 登录检测结果（README check_login_status 工具的返回结构）。
type LoginStatus struct {
	LoggedIn bool
	Message  string
	URL      string
}

// loginTokenCookies 淘宝系（1688）登录态相关 Cookie 名，命中任一即认为存在登录凭证。
var loginTokenCookies = []string{"unb", "cn", "lid", "lgc", "sn", "_tb_token_", "cookie2", "skt"}

// loginStateJS 只读检测页面登录信号：是否有「请登录」类入口、用户头像、
// Cookie 昵称是否出现在页面文本里。不点击不输入，不会触发风控。
const loginStateJS = `(nick) => {
	const text = document.body ? document.body.innerText : '';
	const loginBtn = /(亲，请登录|请登录|登录\/注册|立即登录|免费注册)/.test(text);
	const avatar = !!(document.querySelector('img[class*="avatar"]') ||
		document.querySelector('[class*="avatar"] img') ||
		document.querySelector('img[alt*="头像"]'));
	const nicknameVisible = nick !== '' && text.indexOf(nick) >= 0;
	return JSON.stringify({ loginBtn: loginBtn, avatar: avatar, nicknameVisible: nicknameVisible });
}`

// CheckLoginStatus 多重验证 1688 登录状态，全程只读。首页 URL 本身不体现登录态，
// 所以四项信号缺一不可、综合判定：
//  1. URL 是否被跳转到 login 页（true 即未登录）；顺带识别风控页
//  2. Cookie 是否含登录 token（unb/cn/_tb_token_ 等淘宝系通行凭证）
//  3. 页面是否显示「请登录」类入口（未登录信号）
//  4. 用户昵称（Cookie cn）或头像是否出现在页面上（已登录信号）
func (bm *BrowserManager) CheckLoginStatus() (*LoginStatus, error) {
	page, err := bm.getOrOpenHomePage()
	if err != nil {
		return nil, fmt.Errorf("获取 1688 页面失败: %w", err)
	}
	page = page.Timeout(15 * time.Second)

	// 页面可能正在导航导致 URL 为空，稍候重读一次
	pageURL := ""
	for i := 0; i < 2; i++ {
		if info, err := page.Info(); err == nil {
			pageURL = info.URL
			if pageURL != "" {
				break
			}
		}
		time.Sleep(1 * time.Second)
	}
	if pageURL == "" {
		return nil, fmt.Errorf("读取页面 URL 失败")
	}

	// 1) URL 检测：跳登录页/风控页都视为不可用
	switch {
	case strings.Contains(pageURL, "login"):
		return &LoginStatus{false, "页面跳转到了登录页，请先在浏览器窗口中登录 1688 账号（登录态保存在 user_data 目录）", pageURL}, nil
	case strings.Contains(pageURL, "punish"), strings.Contains(pageURL, "captcha"):
		return &LoginStatus{false, "页面命中了 1688 风控验证，请先在浏览器窗口中完成滑块验证再重试", pageURL}, nil
	}

	// 2) Cookie 检测：是否有登录 token；cn 是用户昵称（URL 编码）
	// 新版 Chrome 已移除 Network.getAllCookies，用 Storage.getCookies
	all, err := proto.StorageGetCookies{}.Call(bm.browser)
	if err != nil {
		return nil, fmt.Errorf("读取 Cookie 失败: %w", err)
	}
	hasToken, nickname := false, ""
	for _, c := range all.Cookies {
		if c.Value == "" {
			continue
		}
		if c.Name == "cn" && nickname == "" {
			if dec, err := url.QueryUnescape(c.Value); err == nil {
				nickname = dec
			} else {
				nickname = c.Value
			}
		}
		if slices.Contains(loginTokenCookies, c.Name) {
			hasToken = true
		}
	}

	// 3)+4) 页面 DOM 检测：登录入口 / 头像 / 昵称可见性
	res, err := page.Eval(loginStateJS, nickname)
	if err != nil {
		return nil, fmt.Errorf("读取页面登录信号失败: %w", err)
	}
	var dom struct {
		LoginBtn        bool `json:"loginBtn"`
		Avatar          bool `json:"avatar"`
		NicknameVisible bool `json:"nicknameVisible"`
	}
	if err := json.Unmarshal([]byte(res.Value.Str()), &dom); err != nil {
		return nil, fmt.Errorf("解析页面登录信号失败: %w", err)
	}

	status := &LoginStatus{URL: pageURL}
	switch {
	case !hasToken:
		status.Message = "未检测到登录 Cookie（unb/cn/_tb_token_ 等），请先在浏览器窗口中登录 1688 账号"
	case dom.LoginBtn && !dom.NicknameVisible && !dom.Avatar:
		status.Message = "检测到登录 Cookie，但页面仍显示「请登录」入口，登录态可能已过期，请重新登录"
	default:
		status.LoggedIn = true
		switch {
		case dom.NicknameVisible:
			status.Message = fmt.Sprintf("已登录（Cookie 登录凭证正常，昵称「%s」已在页面显示）", nickname)
		case dom.Avatar:
			status.Message = "已登录（Cookie 登录凭证正常，页面已显示用户头像）"
		default:
			status.Message = "已登录（Cookie 登录凭证正常，页面无「请登录」入口）"
		}
	}
	return status, nil
}
