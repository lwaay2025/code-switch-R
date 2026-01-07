package services

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

var (
	// globalHTTPClient 全局 HTTP 客户端实例
	globalHTTPClient *http.Client
	// clientMutex 保护全局客户端的并发访问
	clientMutex sync.RWMutex
	// currentProxyConfig 当前代理配置（用于检测配置变化）
	currentProxyConfig ProxyConfig
)

// ProxyConfig 代理配置
type ProxyConfig struct {
	UseProxy     bool
	ProxyAddress string
	ProxyType    string
}

// InitHTTPClient 初始化全局 HTTP 客户端
// 应该在应用启动时调用一次，后续通过 UpdateHTTPClient 更新配置
func InitHTTPClient(config ProxyConfig) error {
	clientMutex.Lock()
	defer clientMutex.Unlock()

	client, err := createHTTPClient(config)
	if err != nil {
		return err
	}

	globalHTTPClient = client
	currentProxyConfig = config
	return nil
}

// UpdateHTTPClient 更新全局 HTTP 客户端的代理配置
// 当用户修改代理设置时调用
func UpdateHTTPClient(config ProxyConfig) error {
	clientMutex.Lock()
	defer clientMutex.Unlock()

	// 如果配置没有变化，不需要重建客户端
	if config == currentProxyConfig {
		return nil
	}

	client, err := createHTTPClient(config)
	if err != nil {
		return err
	}

	globalHTTPClient = client
	currentProxyConfig = config
	return nil
}

// GetHTTPClient 获取全局 HTTP 客户端
// 所有需要发送 HTTP 请求的地方都应该使用此函数获取客户端
func GetHTTPClient() *http.Client {
	clientMutex.RLock()
	defer clientMutex.RUnlock()

	if globalHTTPClient == nil {
		// 如果全局客户端未初始化，返回一个默认客户端
		return createDefaultHTTPClient()
	}

	return globalHTTPClient
}

// GetProxyConfig 获取当前的代理配置
// 用于日志记录和状态检查
func GetProxyConfig() ProxyConfig {
	clientMutex.RLock()
	defer clientMutex.RUnlock()
	return currentProxyConfig
}

// GetHTTPClientWithTimeout 获取带指定超时的 HTTP 客户端
// 这会复制全局客户端的配置但使用新的超时时间
func GetHTTPClientWithTimeout(timeout time.Duration) *http.Client {
	baseClient := GetHTTPClient()

	// 创建一个新的客户端，复用传输层但设置新的超时
	client := &http.Client{
		Transport: baseClient.Transport,
		Timeout:   timeout,
	}

	return client
}

// createHTTPClient 根据配置创建 HTTP 客户端
func createHTTPClient(config ProxyConfig) (*http.Client, error) {
	if !config.UseProxy || config.ProxyAddress == "" {
		// 不使用代理，返回默认客户端
		return createDefaultHTTPClient(), nil
	}

	// 根据代理类型创建相应的传输层
	transport, err := createTransport(config)
	if err != nil {
		return nil, fmt.Errorf("创建代理传输层失败: %w", err)
	}

	return &http.Client{
		Transport: transport,
		Timeout:   32 * time.Hour, // 与现有配置保持一致：32小时超时
	}, nil
}

// createDefaultHTTPClient 创建默认的 HTTP 客户端（不使用代理）
func createDefaultHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: false,
			},
		},
		Timeout: 32 * time.Hour,
	}
}

// createTransport 根据代理类型创建传输层
func createTransport(config ProxyConfig) (http.RoundTripper, error) {
	proxyType := config.ProxyType
	proxyAddr := config.ProxyAddress

	switch proxyType {
	case "http", "https":
		return createHTTPProxyTransport(proxyAddr)
	case "socks5":
		return createSOCKS5ProxyTransport(proxyAddr)
	default:
		return nil, fmt.Errorf("不支持的代理类型: %s", proxyType)
	}
}

// createHTTPProxyTransport 创建 HTTP/HTTPS 代理传输层
func createHTTPProxyTransport(proxyAddr string) (*http.Transport, error) {
	proxyURL, err := url.Parse(proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("解析代理地址失败: %w", err)
	}

	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
		},
	}

	return transport, nil
}

// createSOCKS5ProxyTransport 创建 SOCKS5 代理传输层
func createSOCKS5ProxyTransport(proxyAddr string) (*http.Transport, error) {
	// 解析代理地址
	proxyURL, err := url.Parse(proxyAddr)
	if err != nil {
		// 如果不是完整 URL，尝试作为 host:port 处理
		proxyURL = &url.URL{
			Scheme: "socks5",
			Host:   proxyAddr,
		}
	}

	// 移除 URL scheme，因为 SOCKS5 拨号器只需要 host:port
	socksAddr := proxyURL.Host
	if socksAddr == "" {
		socksAddr = proxyAddr
	}

	// 创建 SOCKS5 拨号器，使用带超时的底层拨号器避免长时间阻塞
	baseDialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	dialer, err := proxy.SOCKS5("tcp", socksAddr, nil, baseDialer)
	if err != nil {
		return nil, fmt.Errorf("创建 SOCKS5 拨号器失败: %w", err)
	}

	// 创建使用 SOCKS5 代理的传输层
	transport := &http.Transport{
		Dial: dialer.Dial,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if ctxDialer, ok := dialer.(proxy.ContextDialer); ok {
				return ctxDialer.DialContext(ctx, network, addr)
			}
			type result struct {
				conn net.Conn
				err  error
			}
			resultCh := make(chan result, 1)
			go func() {
				if ctxErr := ctx.Err(); ctxErr != nil {
					resultCh <- result{conn: nil, err: ctxErr}
					return
				}
				conn, err := dialer.Dial(network, addr)
				if ctx.Err() != nil && conn != nil {
					_ = conn.Close()
					err = ctx.Err()
					conn = nil
				}
				resultCh <- result{conn: conn, err: err}
			}()
			select {
			case res := <-resultCh:
				return res.conn, res.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		ForceAttemptHTTP2:     false, // SOCKS5 通常不支持 HTTP/2
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
		},
	}

	return transport, nil
}

// GetProxyConfigFromSettings 从应用设置中获取代理配置
func GetProxyConfigFromSettings() (ProxyConfig, error) {
	// 创建一个临时的 AppSettingsService 来读取配置
	home, err := getUserHomeDir()
	if err != nil {
		return ProxyConfig{}, fmt.Errorf("获取用户目录失败: %w", err)
	}

	service := &AppSettingsService{
		path: home + "/" + appSettingsDir + "/" + appSettingsFile,
	}

	settings, err := service.GetAppSettings()
	if err != nil {
		return ProxyConfig{}, fmt.Errorf("读取应用设置失败: %w", err)
	}

	return ProxyConfig{
		UseProxy:     settings.UseProxy,
		ProxyAddress: settings.ProxyAddress,
		ProxyType:    settings.ProxyType,
	}, nil
}
