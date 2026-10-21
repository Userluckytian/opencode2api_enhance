package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	AdminPasswordHash string   `json:"admin_password_hash"`
	UpstreamBaseURL   string   `json:"upstream_base_url"`
	UpstreamAPIKey    string   `json:"upstream_api_key"`
	PublicBaseURL     string   `json:"public_base_url"`    // 对外访问地址（领 Key 时下发给客户端），留空则自动推断
	TrustedProxies    []string `json:"trusted_proxies"`    // 可信反代网段，只有这些对端的转发头才被采信
	ProxySecret       string   `json:"proxy_secret"`       // 反代共享密钥：带对才采信 X-Real-IP / X-Forwarded-For
	TTLDays           int      `json:"ttl_days"`
	TokenQuota        int      `json:"token_quota"`
	RateLimitPerMin   int      `json:"rate_limit_per_min"`
	AllowedModels     []string `json:"allowed_models"`
	Port              int      `json:"port"`
	// Listen 是监听地址（仅主机部分，端口由 Port 决定）。
	// 默认仅回环 127.0.0.1：管理面与领取口只经 nginx 反代对外，
	// 显式写 0.0.0.0 才会绑定所有网卡（finding 547 / fact 589、592 的收敛项）。
	// 优先级：命令行 -listen > 环境变量 TOKEN_BUFFET_LISTEN > 本字段。
	Listen string `json:"listen,omitempty"`
	// UpstreamAllowedHosts 是部署方显式放行的上游主机白名单（config.json 静态配置，
	// 管理 API 不可写，避免拿到管理员会话后自行扩白名单）。默认仅回环，
	// 云元数据地址（169.254.169.254 / 100.100.100.200 / fd00:ec2::254）任何情况下不放行。
	UpstreamAllowedHosts []string `json:"upstream_allowed_hosts,omitempty"`
}

// DeprecatedUpstreamAPIKey 是历史版本硬编码的上游默认密钥，已在公开前端包里泄露
// （finding 560）。新配置不再写入，仅用于启动时识别老配置并告警/轮换。
const DeprecatedUpstreamAPIKey = "sk-unified-local"

// EnvUpstreamAPIKey 是部署方注入上游密钥的环境变量名（优先级高于 config.json）。
const EnvUpstreamAPIKey = "TOKEN_BUFFET_UPSTREAM_API_KEY"

func DefaultConfig() *Config {
	return &Config{
		UpstreamBaseURL: "http://127.0.0.1:18080/v1",
		// 不再硬编码上游密钥：由 TOKEN_BUFFET_UPSTREAM_API_KEY 环境变量注入，
		// 或首次启动随机生成（fail-closed：未配置真实密钥时上游必然 401，绝不静默放行）。
		UpstreamAPIKey:  "",
		TTLDays:         1,
		TokenQuota:      30000000,
		RateLimitPerMin: 30,
		Port:            23081,
		// 默认只监听回环：管理面与领取口必须经 nginx 反代对外暴露（finding 547 收敛项）。
		Listen: DefaultListenAddress,
		// 默认仅放行同机回环，兼容现网 http://127.0.0.1:18080/v1。
		UpstreamAllowedHosts: defaultUpstreamAllowedHosts(),
	}
}

func LoadConfig(dir string) (*Config, error) {
	path := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		c := DefaultConfig()
		resolveUpstreamAPIKey(c)
		if err := c.Save(dir); err != nil {
			return nil, err
		}
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	c := DefaultConfig()
	if err := json.Unmarshal(data, c); err != nil {
		return nil, err
	}
	changed := resolveUpstreamAPIKey(c)
	// 对外访问地址（public_base_url）允许由环境变量注入（部署方不落盘明文）。
	// 优先级：config.json > 环境变量 > 空（空 = 拒绝下发 base_url，fail-closed）。
	if env := strings.TrimSpace(os.Getenv(EnvPublicBaseURL)); env != "" {
		if c.PublicBaseURL != env {
			c.PublicBaseURL = env
			changed = true
		}
	}
	if changed {
		if err := c.Save(dir); err != nil {
			log.Printf("warning: 无法回写轮换后的上游密钥: %v", err)
		}
	}
	return c, nil
}

// resolveUpstreamAPIKey 解析上游密钥（finding 560 修复）：
// 优先级：环境变量 TOKEN_BUFFET_UPSTREAM_API_KEY > config.json。
// 若 config.json 仍是历史硬编码的 sk-unified-local（已在公开前端包泄露），
// 或完全没有配置，则用 OS 熵随机生成并落盘，同时明确告警——随机值必然被上游 401，
// 也就是 fail-closed，绝不会再用泄露过的默认密钥静默放行。
// 返回 true 表示配置被修改，需要回写。
func resolveUpstreamAPIKey(c *Config) bool {
	if env := strings.TrimSpace(os.Getenv(EnvUpstreamAPIKey)); env != "" {
		if c.UpstreamAPIKey != env {
			log.Printf("[安全] 上游密钥取自环境变量 %s（末 4 位 %s）", EnvUpstreamAPIKey, maskSecret(env))
			c.UpstreamAPIKey = env
			return true
		}
		return false
	}
	current := strings.TrimSpace(c.UpstreamAPIKey)
	if current != "" && current != DeprecatedUpstreamAPIKey {
		return false
	}
	rotated := GenerateKey()
	if current == DeprecatedUpstreamAPIKey {
		log.Printf("[安全] 检测到已泄露的默认上游密钥 %s，已轮换为随机值（末 4 位 %s）。"+
			"请通过环境变量 %s 注入真实上游密钥！", DeprecatedUpstreamAPIKey, maskSecret(rotated), EnvUpstreamAPIKey)
	} else {
		log.Printf("[安全] 未配置上游密钥，已生成随机占位值（末 4 位 %s）。"+
			"请通过环境变量 %s 注入真实上游密钥，否则 /v1 转发会被上游 401 拒绝（fail-closed）。",
			maskSecret(rotated), EnvUpstreamAPIKey)
	}
	c.UpstreamAPIKey = rotated
	return true
}

func (c *Config) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644)
}
