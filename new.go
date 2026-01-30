// Copyright 2020 ratelimit Author(https://github.com/yudeguang17/gather). All Rights Reserved.
//
// This Source Code Form is subject to the terms of the MIT License.
// If a copy of the MIT was not distributed with this file,
// You can obtain one at https://github.com/yudeguang17/gather.
//
// 模拟浏览器的HTTP数据采集包
// 核心能力：
// 1. 便捷配置HTTP请求头，内置主流浏览器/爬虫UA模板（baidu/google/bing/chrome/360/ie）
// 2. 自动化处理Cookie（依赖外部webCookieJar实现，支持Cookie变更日志）
// 3. 支持普通代理/带认证代理，复用Transport连接池提升高并发性能
// 4. 兼容Go 1.24+，替换弃用的Dial方法为DialContext，避免版本警告
// 5. 全量可配置化：连接池、超时、TLS验证等参数支持运行时调整
package gather

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ---------------------- 全局可配置变量（导出，支持运行时动态调整） ----------------------
// MaxIdleConns 全局HTTP连接池最大空闲连接数（所有主机空闲连接总和）
// 注意事项：
// 1. 该值仅对新创建的Transport生效，已创建的Transport不会自动更新
// 2. 建议根据业务并发量调整，内网高并发场景可设为200+
var MaxIdleConns = 100

// MaxIdleConnsPerHostNoProxy 无代理时单主机最大空闲连接数比例（相对于MaxIdleConns）
// 示例：MaxIdleConns=100，比例0.2 → 单主机最大空闲连接数=20
var MaxIdleConnsPerHostNoProxy = 0.2

// MaxIdleConnsPerHostWithProxy 有代理时单主机最大空闲连接数比例（相对于MaxIdleConns）
// 示例：MaxIdleConns=100，比例0.1 → 单主机最大空闲连接数=10
var MaxIdleConnsPerHostWithProxy = 0.1

// TLSInsecureSkipVerify 是否跳过TLS证书验证（默认true，适配内部服务场景）
// 对接公网服务时，建议手动设置为false以保证传输安全
var TLSInsecureSkipVerify = true

// DialTimeout 网络拨号超时时间（默认10秒，避免连接挂起）
var DialTimeout = 10 * time.Second

// IdleConnTimeout 空闲连接超时时间（默认90秒，释放长期闲置的连接）
var IdleConnTimeout = 90 * time.Second

// TLSHandshakeTimeout TLS握手超时时间（默认10秒）
var TLSHandshakeTimeout = 10 * time.Second

// ExpectContinueTimeout 100-Continue响应超时时间（默认1秒）
var ExpectContinueTimeout = 1 * time.Second

// ResponseHeaderTimeout 响应头读取超时时间（默认15秒，避免等待无响应的服务）
var ResponseHeaderTimeout = 15 * time.Second

// ---------------------- 全局资源与并发控制 ----------------------
// noProxyLocker 无代理Transport单例创建锁，保证线程安全
var noProxyLocker sync.Mutex

// proxyMapLocker 代理Transport缓存池锁，保护transportProxyMap并发读写
var proxyMapLocker sync.Mutex

// transportNoProxy 无代理场景的全局复用Transport（单例）
var transportNoProxy *http.Transport = nil

// transportProxyMap 代理Transport缓存池（key=代理地址，value=对应的Transport）
// 作用：避免重复创建相同代理的Transport，提升性能
var transportProxyMap = make(map[string]*http.Transport)

// ---------------------- 核心结构体定义 ----------------------
// GatherStruct HTTP采集器核心结构体，封装客户端、请求头、Cookie管理
// 最佳实践：
// 1. 每个协程/业务逻辑创建独立实例，避免并发修改Headers导致panic
// 2. 实例创建后，可通过safeHeaders动态修改请求头（并发安全）
type GatherStruct struct {
	Client      *http.Client      // HTTP客户端实例（包含Transport和CookieJar）
	Headers     map[string]string // 基础请求头（初始化时赋值，非并发安全）
	safeHeaders sync.Map          // 并发安全的请求头存储（运行时动态修改用）
	J           *webCookieJar     // Cookie管理器（外部实现，自动处理Cookie）
	locker      sync.Mutex        // 实例级锁，保护结构体字段并发修改
}

// ---------------------- 快捷实例化方法 ----------------------
// NewGather 快捷创建无代理的采集器实例
// 参数：
//
//	defaultAgent: UA类型（支持baidu/google/bing/chrome/360/ie/ie9，空值用Chrome默认）
//	isCookieLogOpen: 是否打印Cookie变更日志（调试场景建议开启）
//
// 返回：初始化完成的采集器实例
// 示例：
//
//	// 创建Chrome UA的采集器，关闭Cookie日志
//	ga := NewGather("chrome", false)
//	// 创建百度爬虫UA的采集器，开启Cookie日志
//	ga := NewGather("baidu", true)
func NewGather(defaultAgent string, isCookieLogOpen bool) *GatherStruct {
	headers := map[string]string{"User-Agent": defaultAgent}
	return NewGatherUtil(headers, "", 300, isCookieLogOpen)
}

// NewGatherProxy 快捷创建带普通代理的采集器实例
// 参数：
//
//	defaultAgent: UA类型（同NewGather）
//	proxyURL: 代理地址（如http://127.0.0.1:8080，空值则无代理）
//	isCookieLogOpen: 是否打印Cookie变更日志
//
// 返回：初始化完成的采集器实例
// 示例：
//
//	ga := NewGatherProxy("chrome", "http://127.0.0.1:8080", false)
func NewGatherProxy(defaultAgent string, proxyURL string, isCookieLogOpen bool) *GatherStruct {
	headers := map[string]string{"User-Agent": defaultAgent}
	return NewGatherUtil(headers, proxyURL, 300, isCookieLogOpen)
}

// ---------------------- 基础实例化方法 ----------------------
// NewGatherUtil 基础版采集器实例化方法（全自定义配置）
// 参数：
//
//	headers: 自定义请求头（仅含User-Agent时自动补全默认浏览器头）
//	proxyURL: 代理地址（空值则无代理）
//	timeOut: 请求超时时间（单位：秒，0表示不设置超时）
//	isCookieLogOpen: 是否打印Cookie变更日志
//
// 返回：初始化完成的采集器实例
// 示例：
//
//	headers := map[string]string{
//	    "Accept":          "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8",
//	    "Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
//	}
//	// 自定义头、无代理、60秒超时、关闭Cookie日志
//	ga := NewGatherUtil(headers, "", 60, false)
func NewGatherUtil(headers map[string]string, proxyURL string, timeOut int, isCookieLogOpen bool) *GatherStruct {
	gather := &GatherStruct{
		Headers: make(map[string]string),
		locker:  sync.Mutex{},
	}

	// 判断是否需要补全默认请求头（仅当headers只有User-Agent时）
	needAddDefaultHeaders := len(headers) == 1
	if needAddDefaultHeaders {
		if _, exist := headers["User-Agent"]; !exist {
			needAddDefaultHeaders = false
		}
	}

	// 补全默认请求头或深拷贝自定义头
	if needAddDefaultHeaders {
		gather.Headers = getDefaultHeaders(headers["User-Agent"])
	} else {
		// 深拷贝：避免外部修改headers影响实例内部
		for k, v := range headers {
			gather.Headers[k] = v
		}
	}

	// 初始化Cookie管理器（外部实现）
	gather.J = newWebCookieJar(isCookieLogOpen)

	// 初始化HTTP客户端
	timeout := time.Duration(timeOut) * time.Second
	if timeOut <= 0 {
		timeout = 0 // 超时为0表示不设置超时
	}
	gather.Client = &http.Client{
		Transport: getHttpTransport(proxyURL),
		Jar:       gather.J,
		Timeout:   timeout,
	}

	// 将请求头同步到并发安全存储
	for k, v := range gather.Headers {
		gather.safeHeaders.Store(k, v)
	}

	return gather
}

// ---------------------- 内部工具方法 ----------------------
// getDefaultHeaders 根据UA类型生成标准化的默认请求头
// 参数：uaType UA类型（baidu/google/bing/chrome/360/ie/ie9）
// 返回：包含完整浏览器头的映射表
func getDefaultHeaders(uaType string) map[string]string {
	defaultHeaders := map[string]string{
		"Accept-Encoding":           "gzip, deflate", // 移除废弃的sdch编码
		"Accept-Language":           "zh-CN,zh;q=0.9,en;q=0.8",
		"Connection":                "keep-alive",
		"Upgrade-Insecure-Requests": "1",
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,image/apng,*/*;q=0.8",
	}

	// 按UA类型设置标准化的User-Agent
	switch strings.ToLower(uaType) {
	case "baidu":
		defaultHeaders["User-Agent"] = "Mozilla/5.0 (compatible; Baiduspider/2.0; +http://www.baidu.com/search/spider.html)"
	case "google":
		defaultHeaders["User-Agent"] = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
	case "bing":
		defaultHeaders["User-Agent"] = "Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)"
	case "chrome":
		defaultHeaders["User-Agent"] = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	case "360":
		defaultHeaders["User-Agent"] = "Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/45.0.2454.101 Safari/537.36"
	case "ie", "ie9":
		defaultHeaders["User-Agent"] = "Mozilla/5.0 (compatible; MSIE 9.0; Windows NT 10.0; Win64; x64; Trident/5.0)"
	case "": // 空值默认使用新版Chrome UA
		defaultHeaders["User-Agent"] = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	default: // 自定义UA直接使用
		defaultHeaders["User-Agent"] = uaType
	}

	return defaultHeaders
}

// ---------------------- Transport管理核心方法 ----------------------
// getHttpTransport 获取/创建Transport实例（复用/缓存机制）
// 核心逻辑：
// 1. 无代理：复用全局单例transportNoProxy
// 2. 有代理：按proxyURL缓存，避免重复创建
// 参数：proxyURL 代理地址（空值则无代理）
// 返回：初始化完成的Transport实例（失败时返回默认Transport）
func getHttpTransport(proxyURL string) *http.Transport {
	// 无代理场景：复用全局单例
	if proxyURL == "" {
		noProxyLocker.Lock()
		defer noProxyLocker.Unlock()

		if transportNoProxy == nil {
			transportNoProxy = newBaseTransport()
			// 计算单主机最大空闲连接数，兜底至少为1
			perHost := int(float64(MaxIdleConns) * MaxIdleConnsPerHostNoProxy)
			transportNoProxy.MaxIdleConnsPerHost = max(perHost, 1)
		}
		return transportNoProxy
	}

	// 有代理场景：从缓存获取或新建
	proxyMapLocker.Lock()
	defer proxyMapLocker.Unlock()

	// 缓存命中直接返回
	if t, ok := transportProxyMap[proxyURL]; ok {
		return t
	}

	// 缓存未命中：创建新Transport
	t := newBaseTransport()
	// 设置代理（补充错误处理，避免解析失败导致panic）
	t.Proxy = func(_ *http.Request) (*url.URL, error) {
		proxyURLParsed, err := url.Parse(proxyURL)
		if err != nil {
			log.Printf("parse proxy url [%s] failed: %v", proxyURL, err)
			return nil, fmt.Errorf("proxy url parse error: %w", err)
		}
		return proxyURLParsed, nil
	}

	// 计算单主机最大空闲连接数，兜底至少为1
	perHost := int(float64(MaxIdleConns) * MaxIdleConnsPerHostWithProxy)
	t.MaxIdleConnsPerHost = max(perHost, 1)

	// 存入缓存
	transportProxyMap[proxyURL] = t
	return t
}

// newBaseTransport 创建基础Transport实例（抽取公共配置，统一管理）
// 核心特性：
// 1. 兼容Go 1.24+：使用DialContext替代弃用的Dial
// 2. 高并发优化：SetLinger(0)避免TIME_WAIT端口堆积
// 3. 全量引用全局配置：便于运行时调整
// 返回：基础配置的Transport实例
func newBaseTransport() *http.Transport {
	return &http.Transport{
		// TLS配置
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: TLSInsecureSkipVerify,
			MinVersion:         tls.VersionTLS12, // 强制TLS 1.2+，提升安全性
		},
		DisableCompression:    true,                // 禁用压缩，避免压缩炸弹攻击
		ForceAttemptHTTP2:     true,                // 优先尝试HTTP2，提升传输效率
		MaxIdleConns:          MaxIdleConns,        // 全局最大空闲连接数
		IdleConnTimeout:       IdleConnTimeout,     // 空闲连接超时
		TLSHandshakeTimeout:   TLSHandshakeTimeout, // TLS握手超时
		ExpectContinueTimeout: ExpectContinueTimeout,
		ResponseHeaderTimeout: ResponseHeaderTimeout,

		// DialContext：替代弃用的Dial，支持Context超时
		DialContext: func(ctx context.Context, netw, addr string) (net.Conn, error) {
			dialer := &net.Dialer{
				Timeout:   DialTimeout,      // 拨号超时
				KeepAlive: 30 * time.Second, // 长连接保活
				DualStack: true,             // 支持IPv4/IPv6双栈
			}

			// 执行拨号，包装错误便于排查
			conn, err := dialer.DialContext(ctx, netw, addr)
			if err != nil {
				return nil, fmt.Errorf("dial [%s://%s] failed: %w", netw, addr, err)
			}

			// 设置TCP Linger=0：避免TIME_WAIT端口堆积（高并发关键）
			if tcpConn, ok := conn.(*net.TCPConn); ok {
				if err := tcpConn.SetLinger(0); err != nil {
					log.Printf("warning: set tcp linger failed for [%s]: %v", addr, err)
				}
			}

			return conn, nil
		},
	}
}

// ---------------------- 带认证代理相关方法 ----------------------
// parseProxyURLWithAuth 解析代理URL并添加基础认证信息
// 功能：
// 1. 自动补全代理URL前缀（缺失http时补全）
// 2. 若有用户名密码，添加到URL的认证字段
// 参数：
//
//	proxyUrl: 原始代理地址（如104.207.139.207:8080、https://104.207.139.207:8080）
//	user: 代理认证用户名（空值则不添加认证）
//	pass: 代理认证密码（空值则不添加认证）
//
// 返回：
//
//	*url.URL: 解析后的代理URL（含认证）
//	error: 解析失败时返回错误
func parseProxyURLWithAuth(proxyUrl, user, pass string) (*url.URL, error) {
	// 补全代理URL前缀
	if !strings.Contains(proxyUrl, "http") {
		proxyUrl = "http://" + proxyUrl
	}

	// 解析代理URL，返回详细错误
	urlProxy, err := url.Parse(proxyUrl)
	if err != nil {
		return nil, fmt.Errorf("parse proxy url [%s] failed: %w", proxyUrl, err)
	}

	// 添加基础认证信息
	if user != "" && pass != "" {
		urlProxy.User = url.UserPassword(user, pass)
	}

	return urlProxy, nil
}

// newBaseTransportWithAuth 创建带认证的代理Transport实例
// 参数：proxyURL 解析后的代理URL（含认证信息）
// 返回：带认证配置的Transport实例
func newBaseTransportWithAuth(proxyURL *url.URL) *http.Transport {
	return &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: TLSInsecureSkipVerify,
			MinVersion:         tls.VersionTLS12,
		},
		DisableCompression:    true,
		ForceAttemptHTTP2:     true,
		Proxy:                 http.ProxyURL(proxyURL), // 带认证的代理配置
		MaxIdleConns:          MaxIdleConns,
		MaxIdleConnsPerHost:   MaxIdleConns, // 代理场景单主机复用全部空闲连接
		IdleConnTimeout:       IdleConnTimeout,
		TLSHandshakeTimeout:   TLSHandshakeTimeout,
		ExpectContinueTimeout: ExpectContinueTimeout,

		// 复用DialContext逻辑，保证一致性
		DialContext: func(ctx context.Context, netw, addr string) (net.Conn, error) {
			dialer := &net.Dialer{
				Timeout:   DialTimeout,
				KeepAlive: 30 * time.Second,
				DualStack: true,
			}

			conn, err := dialer.DialContext(ctx, netw, addr)
			if err != nil {
				return nil, fmt.Errorf("dial proxy [%s://%s] failed: %w", netw, addr, err)
			}

			if tcpConn, ok := conn.(*net.TCPConn); ok {
				if err := tcpConn.SetLinger(0); err != nil {
					log.Printf("warning: set tcp linger failed for proxy [%s]: %v", addr, err)
				}
			}

			return conn, nil
		},
	}
}

// NewGatherProxyHasPassUtil 快捷创建带认证代理的采集器实例
// 参数：
//
//	headers: 自定义请求头（仅含User-Agent时自动补全默认头）
//	proxyURL: 代理地址（如104.207.139.207:8080）
//	user: 代理认证用户名
//	pass: 代理认证密码
//	isCookieLogOpen: 是否打印Cookie变更日志
//
// 返回：初始化完成的采集器实例（超时默认300秒）
// 示例：
//
//	headers := map[string]string{"User-Agent": "chrome"}
//	ga := NewGatherProxyHasPassUtil(headers, "104.207.139.207:8080", "admin", "123456", false)
func NewGatherProxyHasPassUtil(headers map[string]string, proxyURL, user, pass string, isCookieLogOpen bool) *GatherStruct {
	return NewGatherUtilHasPass(headers, proxyURL, user, pass, 300, isCookieLogOpen)
}

// getHttpTransportHasPass 创建带认证的代理Transport实例
// 特性：代理频繁更换场景下不复用，每次新建实例
// 参数：
//
//	proxyUrl: 代理地址
//	user: 代理认证用户名
//	pass: 代理认证密码
//
// 返回：带认证的Transport实例（失败时返回nil）
func getHttpTransportHasPass(proxyUrl, user, pass string) *http.Transport {
	// 解析代理URL并添加认证
	urlProxy, err := parseProxyURLWithAuth(proxyUrl, user, pass)
	if err != nil {
		log.Printf("parse auth proxy url failed: %v", err)
		return nil
	}

	// 创建带认证的Transport
	return newBaseTransportWithAuth(urlProxy)
}

// NewGatherUtilHasPass 基础创建带认证代理的采集器实例
// 参数：
//
//	headers: 自定义请求头
//	proxyURL: 代理地址
//	user: 代理认证用户名
//	pass: 代理认证密码
//	timeOut: 请求超时时间（单位：秒，0表示不设置超时）
//	isCookieLogOpen: 是否打印Cookie变更日志
//
// 返回：初始化完成的采集器实例
// 示例：
//
//	headers := map[string]string{
//	    "Accept-Language": "zh-CN,zh;q=0.9",
//	    "User-Agent":      "chrome",
//	}
//	ga := NewGatherUtilHasPass(headers, "104.207.139.207:8080", "admin", "123456", 60, false)
func NewGatherUtilHasPass(headers map[string]string, proxyURL, user, pass string, timeOut int, isCookieLogOpen bool) *GatherStruct {
	gather := &GatherStruct{
		Headers: make(map[string]string),
		locker:  sync.Mutex{},
	}

	// 处理默认请求头
	needAddDefaultHeaders := len(headers) == 1
	if needAddDefaultHeaders {
		if _, exist := headers["User-Agent"]; !exist {
			needAddDefaultHeaders = false
		}
	}
	if needAddDefaultHeaders {
		gather.Headers = getDefaultHeaders(headers["User-Agent"])
	} else {
		for k, v := range headers {
			gather.Headers[k] = v
		}
	}

	// 初始化Cookie管理器
	gather.J = newWebCookieJar(isCookieLogOpen)

	// 处理超时（0表示不设置超时）
	timeout := time.Duration(timeOut) * time.Second
	if timeOut <= 0 {
		timeout = 0
	}

	// 创建带认证的Transport
	transport := getHttpTransportHasPass(proxyURL, user, pass)
	if transport == nil {
		// Transport创建失败时，使用基础Client避免panic
		gather.Client = &http.Client{Timeout: timeout}
		log.Printf("create auth proxy transport failed, use basic client")
	} else {
		gather.Client = &http.Client{
			Transport: transport,
			Jar:       gather.J,
			Timeout:   timeout,
		}
	}

	// 同步请求头到并发安全存储
	for k, v := range gather.Headers {
		gather.safeHeaders.Store(k, v)
	}

	return gather
}
