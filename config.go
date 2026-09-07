package main

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 服务运行配置。
// 来源优先级：环境变量（PORT/HEADLESS）> config.yaml > 默认值。
type Config struct {
	Port           string        // HTTP 监听端口
	Headless       bool          // 是否无头运行浏览器
	UserDataDir    string        // 浏览器登录态目录
	ResultWait     time.Duration // 等待搜索结果出现的时长
	CaptchaWait    time.Duration // 风控验证/登录页的人工等待时长
	PageTimeout    time.Duration // 单页操作超时上限
	DetailInterval time.Duration // 相邻两次商品详情抓取的最小间隔（防风控限速）
}

// yamlConfig 是 config.yaml 的磁盘结构（超时以秒为单位，便于阅读）。
type yamlConfig struct {
	Port        string `yaml:"port"`
	Headless    *bool  `yaml:"headless"`
	UserDataDir string `yaml:"user_data_dir"`
	Timeouts    struct {
		ResultWait     int `yaml:"result_wait"`     // 秒
		CaptchaWait    int `yaml:"captcha_wait"`    // 秒
		PageTimeout    int `yaml:"page_timeout"`    // 秒
		DetailInterval int `yaml:"detail_interval"` // 秒，两次详情抓取的最小间隔
	} `yaml:"timeouts"`
}

// loadConfig 读取配置文件（文件不存在时用默认值）并应用环境变量覆盖。
func loadConfig(path string) (*Config, error) {
	cfg := &Config{
		Port:           "18080",
		Headless:       false,
		UserDataDir:    "./user_data",
		ResultWait:     20 * time.Second,
		CaptchaWait:    60 * time.Second,
		PageTimeout:    90 * time.Second,
		DetailInterval: 30 * time.Second,
	}

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		var yc yamlConfig
		if err := yaml.Unmarshal(raw, &yc); err != nil {
			return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
		}
		if yc.Port != "" {
			cfg.Port = yc.Port
		}
		if yc.Headless != nil {
			cfg.Headless = *yc.Headless
		}
		if yc.UserDataDir != "" {
			cfg.UserDataDir = yc.UserDataDir
		}
		if yc.Timeouts.ResultWait > 0 {
			cfg.ResultWait = time.Duration(yc.Timeouts.ResultWait) * time.Second
		}
		if yc.Timeouts.CaptchaWait > 0 {
			cfg.CaptchaWait = time.Duration(yc.Timeouts.CaptchaWait) * time.Second
		}
		if yc.Timeouts.PageTimeout > 0 {
			cfg.PageTimeout = time.Duration(yc.Timeouts.PageTimeout) * time.Second
		}
		if yc.Timeouts.DetailInterval > 0 {
			cfg.DetailInterval = time.Duration(yc.Timeouts.DetailInterval) * time.Second
		}
	case os.IsNotExist(err):
		// 没有配置文件，全部用默认值
	default:
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}

	// 环境变量优先级最高
	if v := os.Getenv("PORT"); v != "" {
		cfg.Port = v
	}
	if v := os.Getenv("HEADLESS"); v != "" {
		cfg.Headless = parseBool(v)
	}
	return cfg, nil
}
