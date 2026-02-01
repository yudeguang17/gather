// Copyright 2020 ratelimit Author(https://github.com/yudeguang/gather). All Rights Reserved.
//
// This Source Code Form is subject to the terms of the MIT License.
// If a copy of the MIT was not distributed with this file,
// You can obtain one at https://github.com/yudeguang/gather.
//
// 模拟浏览器进行HTTP数据采集包
// 核心特性：
// 1. 可配置化：支持慢速/快速连接配置，适配不同响应速度的网站
// 2. 自动化Cookie处理：内置CookieJar，自动管理Cookie生命周期
// 3. 灵活的代理支持：兼容普通代理/带认证代理，支持代理动态切换
// 4. 连接池优化：合理的空闲连接管理，平衡性能与资源占用
// 5. 自动配置：根据总超时自动推导各阶段细分超时，简化配置成本
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

// ---------------------- 核心配置结构体（详细注释版） ----------------------
// GatherConfig 采集器核心配置结构体，集中管理所有连接/超时参数
// 每个字段均标注：作用+默认值+取值范围+场景建议，便于精准配置
type GatherConfig struct {
	// 连接池配置（影响长连接复用效率）
	// MaxIdleConns：全局HTTP连接池的最大空闲连接总数（所有主机的空闲连接之和≤此值）
	// - 默认值（慢/快配置）：100
	// - 取值范围：建议10~1000（过高易导致端口耗尽，过低影响并发）
	// - 场景建议：
	//   慢连接/低并发：50~200；高并发/快连接：200~500
	MaxIdleConns int

	// MaxIdleConnsPerHost：单主机允许的最大空闲连接数（长连接复用核心参数）
	// - 默认值（慢/快配置）：100
	// - 取值范围：建议≥MaxIdleConns的50%（单主机场景）；多主机场景设为MaxIdleConns/主机数
	// - 场景建议：
	//   单主机采集（如仅抓一个网站）：等于MaxIdleConns；多主机采集：10~50
	MaxIdleConnsPerHost int

	// IdleConnTimeout：空闲连接在连接池中保留的最长时间，超时自动关闭
	// - 默认值：慢速配置90秒；快速配置30秒
	// - 取值范围：10秒~5分钟
	// - 场景建议：
	//   慢连接：60~120秒（保留长连接，减少重连开销）；快连接：10~30秒（快速释放资源）
	IdleConnTimeout time.Duration

	// TLS配置（影响HTTPS连接安全性与兼容性）
	// TLSInsecureSkipVerify：是否跳过TLS证书验证（如自签名/过期证书场景）
	// - 默认值：true
	// - 取值范围：true/false
	// - 场景建议：
	//   内网/测试环境：true（忽略证书问题）；公网生产环境：false（强制验证，保证安全）
	TLSInsecureSkipVerify bool

	// 超时配置（按连接阶段分类，核心影响采集超时逻辑）
	// DialTimeout：TCP网络拨号超时（建立TCP三次握手的超时时间）
	// - 默认值：慢速配置30秒；快速配置5秒
	// - 取值范围：1秒~60秒
	// - 场景建议：
	//   慢连接/海外网站：10~30秒；快连接/内网：1~5秒
	DialTimeout time.Duration

	// TLSHandshakeTimeout：TLS/HTTPS握手超时时间（完成SSL/TLS密钥交换的超时）
	// - 默认值：慢速配置30秒；快速配置5秒
	// - 取值范围：1秒~60秒
	// - 场景建议：
	//   慢HTTPS服务器：10~30秒；快HTTPS服务器：1~5秒
	TLSHandshakeTimeout time.Duration

	// ExpectContinueTimeout：发送Expect: 100-continue后，等待服务器响应的超时（主要影响POST大请求）
	// - 默认值：慢速配置10秒；快速配置1秒
	// - 取值范围：0.5秒~30秒
	// - 场景建议：
	//   大请求/慢服务器：5~10秒；小请求/快服务器：0.5~2秒
	ExpectContinueTimeout time.Duration

	// ResponseHeaderTimeout：从TCP连接建立到服务器返回响应头的超时（0=无限等待）
	// 核心参数！决定是否支持慢响应网站
	// - 默认值：慢速配置0秒；快速配置5秒
	// - 取值范围：0秒（无限）~300秒
	// - 场景建议：
	//   慢响应网站（动态生成页面）：0秒（靠Client.Timeout兜底）；快响应网站：3~10秒（快速失败）
	ResponseHeaderTimeout time.Duration

	// 连接优化配置（平衡性能、兼容性、资源占用）
	// DisableCompression：是否禁用HTTP压缩（gzip/deflate）
	// - 默认值：慢速配置false；快速配置true
	// - 取值范围：true/false
	// - 场景建议：
	//   慢连接/带宽有限：false（启用压缩，减少传输量）；快连接/CPU紧张：true（减少CPU开销）
	DisableCompression bool

	// ForceAttemptHTTP2：是否强制尝试使用HTTP2协议（即使服务器不支持）
	// - 默认值：慢速配置false；快速配置true
	// - 取值范围：true/false
	// - 场景建议：
	//   老旧服务器/慢连接：false（HTTP1.1兼容性更好）；现代服务器/快连接：true（HTTP2多路复用更快）
	ForceAttemptHTTP2 bool

	// TCPLinger：TCP连接关闭时的Linger参数（SO_LINGER），单位秒
	// 0=立即关闭，不等待未发送的数据包；>0=等待指定秒数，保证数据完整发送
	// - 默认值：慢速配置1秒；快速配置0秒
	// - 取值范围：0~10秒
	// - 场景建议：
	//   慢连接/易丢包：1~3秒（等待数据包发送完成）；高并发/快连接：0秒（减少TIME_WAIT端口堆积）
	TCPLinger int

	// KeepAlive：TCP长连接保活时间，每隔此时间发送保活探测包检测连接是否存活
	// - 默认值：慢速配置60秒；快速配置30秒
	// - 取值范围：10秒~5分钟
	// - 场景建议：
	//   长连接/慢连接：30~60秒（减少重连）；短连接/快连接：10~30秒（快速检测无效连接）
	KeepAlive time.Duration
}

// ---------------------- 全局配置管理（核心函数+详细注释） ----------------------
var (
	globalConfig     *GatherConfig   // 全局默认配置（初始化时设为慢速配置）
	configLocker     sync.RWMutex    // 配置读写锁（保证并发安全）
	transportLocker  sync.Mutex      // Transport创建锁（保护单例）
	transportNoProxy *http.Transport // 无代理Transport单例（复用长连接）
)

// 包初始化：默认启用慢速连接配置，适配大多数慢响应网站场景
func init() {
	UseSlowConnConfig()
}

// SetGatherConfig 手动设置全局采集器配置，参数不合理会直接panic（强制校验）
// 适用场景：需要精细化调整配置参数，适配特殊场景（如海外慢站、高并发快站）
// 校验规则（违反任意一条即panic）：
// 1. MaxIdleConns/MaxIdleConnsPerHost 必须>0（连接池不能为0）
// 2. IdleConnTimeout/KeepAlive 必须>0（空闲连接/保活时间不能为0）
// 3. 所有超时参数（Dial/TLSHandshake等）必须≥0（超时不能为负数）
// 4. TCPLinger 必须≥0（Linger参数不能为负数）
// 5. 配置对象不能为nil
// 配置生效：调用后新创建的采集器立即使用新配置，已创建的采集器不受影响
func SetGatherConfig(cfg *GatherConfig) {
	if cfg == nil {
		panic("SetGatherConfig: 配置对象cfg不能为nil")
	}

	// 严格参数校验，收集所有错误信息后统一panic
	var errMsgs []string
	if cfg.MaxIdleConns <= 0 {
		errMsgs = append(errMsgs, fmt.Sprintf("MaxIdleConns必须>0（当前值：%d）", cfg.MaxIdleConns))
	}
	if cfg.MaxIdleConnsPerHost <= 0 {
		errMsgs = append(errMsgs, fmt.Sprintf("MaxIdleConnsPerHost必须>0（当前值：%d）", cfg.MaxIdleConnsPerHost))
	}
	if cfg.IdleConnTimeout <= 0 {
		errMsgs = append(errMsgs, fmt.Sprintf("IdleConnTimeout必须>0（当前值：%v）", cfg.IdleConnTimeout))
	}
	if cfg.DialTimeout < 0 {
		errMsgs = append(errMsgs, fmt.Sprintf("DialTimeout必须≥0（当前值：%v）", cfg.DialTimeout))
	}
	if cfg.TLSHandshakeTimeout < 0 {
		errMsgs = append(errMsgs, fmt.Sprintf("TLSHandshakeTimeout必须≥0（当前值：%v）", cfg.TLSHandshakeTimeout))
	}
	if cfg.ExpectContinueTimeout < 0 {
		errMsgs = append(errMsgs, fmt.Sprintf("ExpectContinueTimeout必须≥0（当前值：%v）", cfg.ExpectContinueTimeout))
	}
	if cfg.ResponseHeaderTimeout < 0 {
		errMsgs = append(errMsgs, fmt.Sprintf("ResponseHeaderTimeout必须≥0（当前值：%v）", cfg.ResponseHeaderTimeout))
	}
	if cfg.TCPLinger < 0 {
		errMsgs = append(errMsgs, fmt.Sprintf("TCPLinger必须≥0（当前值：%d）", cfg.TCPLinger))
	}
	if cfg.KeepAlive <= 0 {
		errMsgs = append(errMsgs, fmt.Sprintf("KeepAlive必须>0（当前值：%v）", cfg.KeepAlive))
	}

	// 校验失败则panic，强制保证配置合理性
	if len(errMsgs) > 0 {
		panic(fmt.Sprintf("SetGatherConfig: 配置参数不合法：%s", strings.Join(errMsgs, "；")))
	}

	// 加锁更新全局配置，并重置无代理Transport单例（使新配置生效）
	configLocker.Lock()
	defer configLocker.Unlock()

	transportLocker.Lock()
	transportNoProxy = nil // 重置单例，下次创建Transport会使用新配置
	transportLocker.Unlock()

	globalConfig = cfg
}

// UseSlowConnConfig 一键切换到默认慢速连接配置（核心适配慢响应网站）
// 底层调用SetGatherConfigByClientTimeout，默认总超时10分钟
// 适用场景：
// 1. 响应速度慢的网站（如动态生成页面、海外网站、服务器处理耗时久）
// 2. 允许等待较长时间获取数据，优先保证采集成功率
// 核心配置特点：
// - ResponseHeaderTimeout=0（无限等待响应头）
// - 拨号/TLS握手超时放宽至30秒
// - 启用压缩、关闭强制HTTP2、TCP Linger=1秒（保证数据完整性）
func UseSlowConnConfig() {
	// 默认慢速配置：总超时10分钟，慢连接模式，跳过TLS验证
	SetGatherConfigByClientTimeout(10*time.Minute, true, true)
}

// UseFastConnConfig 一键切换到默认快速连接配置（核心适配快速响应网站）
// 底层调用SetGatherConfigByClientTimeout，默认总超时30秒
// 适用场景：
// 1. 响应速度快的网站（如静态页面、内网服务、高性能API）
// 2. 要求快速失败，优先保证采集效率，不接受长时间等待
// 核心配置特点：
// - ResponseHeaderTimeout=5秒（快速失败）
// - 所有超时参数收紧至1~5秒
// - 禁用压缩、启用HTTP2、TCP Linger=0秒（快速释放资源）
func UseFastConnConfig() {
	// 默认快速配置：总超时30秒，快连接模式，跳过TLS验证
	SetGatherConfigByClientTimeout(30*time.Second, false, true)
}

// SetGatherConfigByClientTimeout 根据Client总超时自动生成并设置所有细分超时配置
// 核心逻辑：按请求阶段合理分配总超时，同时保证每个阶段有最小保底值
// 参数说明：
//
//	totalTimeout: Client.Timeout总超时（比如10*time.Second）
//	isSlowConn: 是否适配慢连接场景（true=慢连接，false=快连接）
//	tlsInsecure: 是否跳过TLS证书验证（默认true）
//
// 使用示例：
//
//	// 配置总超时10秒的快连接
//	SetGatherConfigByClientTimeout(10*time.Second, false, true)
//	// 配置总超时10分钟的慢连接
//	SetGatherConfigByClientTimeout(10*time.Minute, true, true)
func SetGatherConfigByClientTimeout(totalTimeout time.Duration, isSlowConn bool, tlsInsecure bool) {
	// 校验总超时合法性
	if totalTimeout <= 0 {
		panic("SetGatherConfigByClientTimeout: totalTimeout必须>0（总超时不能为0或负数）")
	}

	// 定义各阶段最小保底值（避免分配过小）
	minDialTimeout := 1 * time.Second
	minTLSTimeout := 1 * time.Second
	minExpectTimeout := 500 * time.Millisecond
	minResponseHeaderTimeout := 2 * time.Second

	// 按场景分配各阶段占比
	var (
		dialRatio           float64 // 拨号超时占比
		tlsRatio            float64 // TLS握手超时占比
		expectRatio         float64 // Expect超时占比
		responseHeaderRatio float64 // 响应头超时占比
		idleConnTimeout     time.Duration
		keepAlive           time.Duration
		disableCompression  bool
		forceHTTP2          bool
		tcpLinger           int
	)

	// 慢连接场景：给响应头更多占比，放宽各阶段超时
	if isSlowConn {
		dialRatio = 0.2           // 拨号超时占总超时20%
		tlsRatio = 0.2            // TLS握手占20%
		expectRatio = 0.1         // Expect超时占10%
		responseHeaderRatio = 0.4 // 响应头超时占40%（核心）
		// 慢连接其他配置
		idleConnTimeout = 90 * time.Second
		keepAlive = 60 * time.Second
		disableCompression = false // 启用压缩
		forceHTTP2 = false         // 禁用HTTP2
		tcpLinger = 1              // 等待1秒保证数据完整
	} else {
		// 快连接场景：收紧所有阶段占比，快速失败
		dialRatio = 0.2           // 拨号超时占20%
		tlsRatio = 0.2            // TLS握手占20%
		expectRatio = 0.1         // Expect超时占10%
		responseHeaderRatio = 0.4 // 响应头超时占40%
		// 快连接其他配置
		idleConnTimeout = 30 * time.Second
		keepAlive = 30 * time.Second
		disableCompression = true // 禁用压缩
		forceHTTP2 = true         // 启用HTTP2
		tcpLinger = 0             // 立即关闭连接
	}

	// 计算各阶段超时（取“按比例分配值”和“最小保底值”的较大者）
	dialTimeout := time.Duration(float64(totalTimeout) * dialRatio)
	if dialTimeout < minDialTimeout {
		dialTimeout = minDialTimeout
	}

	tlsHandshakeTimeout := time.Duration(float64(totalTimeout) * tlsRatio)
	if tlsHandshakeTimeout < minTLSTimeout {
		tlsHandshakeTimeout = minTLSTimeout
	}

	expectContinueTimeout := time.Duration(float64(totalTimeout) * expectRatio)
	if expectContinueTimeout < minExpectTimeout {
		expectContinueTimeout = minExpectTimeout
	}

	responseHeaderTimeout := time.Duration(float64(totalTimeout) * responseHeaderRatio)
	if responseHeaderTimeout < minResponseHeaderTimeout {
		responseHeaderTimeout = minResponseHeaderTimeout
	}

	// 慢连接特殊处理：响应头超时设为0（无限等），靠totalTimeout兜底
	if isSlowConn {
		responseHeaderTimeout = 0 // 核心：无限等待响应头，总超时靠Client.Timeout控制
	}

	// 连接池配置（固定值，按场景优化）
	maxIdleConns := 100
	maxIdleConnsPerHost := 100

	// 组装配置并调用SetGatherConfig（自动触发参数校验）
	cfg := &GatherConfig{
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		IdleConnTimeout:       idleConnTimeout,
		TLSInsecureSkipVerify: tlsInsecure,
		DialTimeout:           dialTimeout,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ExpectContinueTimeout: expectContinueTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		DisableCompression:    disableCompression,
		ForceAttemptHTTP2:     forceHTTP2,
		TCPLinger:             tcpLinger,
		KeepAlive:             keepAlive,
	}

	// 调用原有函数，自动校验并生效
	SetGatherConfig(cfg)
}

// ---------------------- 采集器核心结构体（保留原有逻辑+注释） ----------------------
// GatherStruct 采集器核心结构体，封装HTTP客户端、请求头、Cookie管理
// 使用建议：
// 1. 每个协程/业务逻辑创建独立实例，避免并发修改Headers导致panic
// 2. 实例创建后可通过safeHeaders动态修改请求头（并发安全）
// 3. 慢连接场景需手动设置Client.Timeout（如10分钟），作为最终兜底超时
type GatherStruct struct {
	Client      *http.Client      // HTTP客户端实例（包含Transport和CookieJar）
	Headers     map[string]string // 基础请求头（初始化时赋值，非并发安全）
	safeHeaders sync.Map          // 并发安全的请求头存储（运行时动态修改）
	J           *webCookieJar     // Cookie管理器（自动处理Cookie生命周期）
	locker      sync.Mutex        // 实例级锁，保护结构体字段并发修改
}

// NewGather 快捷创建无代理的采集器实例（默认启用慢速配置）
// 参数说明：
//
//	defaultAgent: UA类型（支持baidu/google/bing/chrome/360/ie/ie9，空值用Chrome默认）
//	isCookieLogOpen: Cookie变更时是否打印日志（调试场景建议开启）
//
// 使用示例：
//
//	ga := NewGather("chrome", false)
//	ga.Client.Timeout = 10 * time.Minute // 慢连接场景设置兜底超时
func NewGather(defaultAgent string, isCookieLogOpen bool) *GatherStruct {
	var headers = make(map[string]string)
	headers["User-Agent"] = defaultAgent
	return NewGatherUtil(headers, "", 300, isCookieLogOpen)
}

// NewGatherProxy 快捷创建带普通代理的采集器实例（默认启用慢速配置）
// 参数说明：
//
//	defaultAgent: UA类型（同NewGather）
//	proxyURL: 代理地址（如http://127.0.0.1:8080，空值则无代理）
//	isCookieLogOpen: Cookie变更时是否打印日志
//
// 使用示例：
//
//	ga := NewGatherProxy("chrome", "http://127.0.0.1:8080", false)
//	ga.Client.Timeout = 10 * time.Minute // 慢连接场景设置兜底超时
func NewGatherProxy(defaultAgent string, proxyURL string, isCookieLogOpen bool) *GatherStruct {
	var headers = make(map[string]string)
	headers["User-Agent"] = defaultAgent
	return NewGatherUtil(headers, proxyURL, 300, isCookieLogOpen)
}

// NewGatherUtil 最基础的采集器实例化方法（自定义请求头/代理/超时）
// 参数说明：
//
//	headers: 自定义Request Headers（仅传User-Agent时自动补全默认浏览器头）
//	proxyURL: 代理服务器地址，无需代理则留空
//	timeOut: 采集超时时间（单位：秒，0表示不设置，建议慢连接设为600秒）
//	isCookieLogOpen: Cookie变更时是否打印日志
//
// 使用示例：
//
//	headers := map[string]string{
//	    "Accept":          "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8",
//	    "Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
//	    "User-Agent":      "chrome",
//	}
//	ga := NewGatherUtil(headers, "", 600, false) // 10分钟超时，适配慢连接
func NewGatherUtil(headers map[string]string, proxyURL string, timeOut int, isCookieLogOpen bool) *GatherStruct {
	var gather GatherStruct
	gather.Headers = make(map[string]string)

	// 自动补全默认请求头（仅当headers仅包含User-Agent时触发）
	if len(headers) == 1 {
		if v, exist := headers["User-Agent"]; exist {
			var defaultHeaders = make(map[string]string)
			defaultHeaders["Accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8"
			defaultHeaders["Accept-Encoding"] = "gzip, deflate, sdch"
			defaultHeaders["Accept-Language"] = "zh-CN,zh;q=0.8"
			defaultHeaders["Connection"] = "keep-alive"
			defaultHeaders["Upgrade-Insecure-Requests"] = "1"

			// 根据UA类型设置标准化User-Agent
			switch strings.ToLower(v) {
			case "baidu":
				defaultHeaders["User-Agent"] = "Mozilla/5.0 (compatible; Baiduspider/2.0;++http://www.baidu.com/search/spider.html)"
			case "google":
				defaultHeaders["User-Agent"] = "Mozilla/5.0 (compatible; Googlebot/2.1;+http://www.google.com/bot.html)"
			case "bing":
				defaultHeaders["User-Agent"] = "Mozilla/5.0 (compatible; bingbot/2.0;+http://www.bing.com/bingbot.htm)"
			case "chrome":
				defaultHeaders["User-Agent"] = "Mozilla/5.0 (Windows NT 6.1; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/56.0.2924.87 Safari/537.36"
			case "360":
				defaultHeaders["User-Agent"] = "Mozilla/5.0 (Windows NT 6.1; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/45.0.2454.101 Safari/537.36"
			case "ie", "ie9":
				defaultHeaders["User-Agent"] = "Mozilla/5.0 (compatible; MSIE 9.0; Windows NT 6.1; Win64; x64; Trident/5.0)"
			case "": // 空值默认使用Chrome UA
				defaultHeaders["User-Agent"] = "Mozilla/5.0 (Windows NT 6.1; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/56.0.2924.87 Safari/537.36"
			default: // 自定义UA直接使用
				defaultHeaders["User-Agent"] = v
			}
			gather.Headers = defaultHeaders
		} else {
			gather.Headers = headers
		}
	} else {
		gather.Headers = headers
	}

	// 初始化Cookie管理器和HTTP客户端
	gather.J = newWebCookieJar(isCookieLogOpen)
	gather.Client = &http.Client{Transport: getHttpTransport(proxyURL), Jar: gather.J}
	gather.Client.Timeout = time.Duration(timeOut) * time.Second

	// 将请求头同步到并发安全存储
	for k, v := range gather.Headers {
		gather.safeHeaders.Store(k, v)
	}
	return &gather
}

// ---------------------- Transport创建逻辑（基于全局配置） ----------------------
// getHttpTransport 根据当前全局配置创建HTTP Transport实例
// 核心逻辑：
// 1. 无代理场景：复用全局单例transportNoProxy（减少连接创建开销）
// 2. 有代理场景：每次新建Transport（适配代理动态切换）
// 3. 配置变更时会重置单例，保证新配置生效
func getHttpTransport(proxyURL string) *http.Transport {
	// 读锁获取当前全局配置（并发安全）
	configLocker.RLock()
	cfg := globalConfig
	configLocker.RUnlock()

	transportLocker.Lock()
	defer transportLocker.Unlock()

	// 无代理场景：复用单例
	if proxyURL == "" {
		if transportNoProxy == nil {
			transportNoProxy = newTransport(cfg, nil)
		}
		return transportNoProxy
	}

	// 有代理场景：每次新建（代理可能频繁更换）
	proxyFunc := func(_ *http.Request) (*url.URL, error) {
		return url.Parse(proxyURL)
	}
	return newTransport(cfg, proxyFunc)
}

// newTransport 基于指定配置创建HTTP Transport实例
// 参数说明：
//
//	cfg: 采集器配置（决定Transport的所有行为）
//	proxy: 代理函数（nil表示无代理）
//
// 核心优化：
// 1. 使用DialContext替代弃用的Dial（兼容Go 1.24+）
// 2. 强制TLS 1.2+，提升HTTPS安全性
// 3. 严格遵循配置参数，保证行为可预期
func newTransport(cfg *GatherConfig, proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	transport := &http.Transport{
		// TLS配置（强制TLS 1.2+，提升安全性）
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: cfg.TLSInsecureSkipVerify,
			MinVersion:         tls.VersionTLS12,
		},

		// 连接池配置
		MaxIdleConns:        cfg.MaxIdleConns,
		MaxIdleConnsPerHost: cfg.MaxIdleConnsPerHost,
		IdleConnTimeout:     cfg.IdleConnTimeout,

		// 超时配置
		TLSHandshakeTimeout:   cfg.TLSHandshakeTimeout,
		ExpectContinueTimeout: cfg.ExpectContinueTimeout,
		ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,

		// 连接优化配置
		DisableCompression: cfg.DisableCompression,
		ForceAttemptHTTP2:  cfg.ForceAttemptHTTP2,

		// DialContext：替代弃用的Dial，支持上下文超时
		DialContext: func(ctx context.Context, netw, addr string) (net.Conn, error) {
			dialer := &net.Dialer{
				Timeout:   cfg.DialTimeout,
				KeepAlive: cfg.KeepAlive,
				DualStack: true, // 支持IPv4/IPv6双栈
			}

			// 执行TCP拨号
			conn, err := dialer.DialContext(ctx, netw, addr)
			if err != nil {
				return nil, err
			}

			// 设置TCP Linger参数（保证慢连接数据完整性）
			if tcpConn, ok := conn.(*net.TCPConn); ok {
				_ = tcpConn.SetLinger(cfg.TCPLinger)
			}
			return conn, nil
		},
	}

	// 设置代理（如有）
	if proxy != nil {
		transport.Proxy = proxy
	}

	return transport
}

// ---------------------- 带认证代理相关方法 ----------------------
// NewGatherProxyHasPassUtil 快捷创建带认证代理的采集器实例
// 参数说明：
//
//	headers: 自定义请求头
//	proxyURL: 代理地址（如104.207.139.207:8080）
//	user: 代理认证用户名
//	pass: 代理认证密码
//	isCookieLogOpen: Cookie变更时是否打印日志
//
// 使用示例：
//
//	headers := map[string]string{"User-Agent": "chrome"}
//	ga := NewGatherProxyHasPassUtil(headers, "104.207.139.207:8080", "admin", "123456", false)
func NewGatherProxyHasPassUtil(headers map[string]string, proxyURL, user, pass string, isCookieLogOpen bool) *GatherStruct {
	return NewGatherUtilHasPass(headers, proxyURL, user, pass, 300, isCookieLogOpen)
}

// getHttpTransportHasPass 创建带认证的代理Transport实例
// 核心逻辑：
// 1. 自动补全代理URL前缀（缺失http时补全）
// 2. 添加用户名密码到代理URL的认证字段
// 3. 基于当前全局配置创建Transport，保证配置一致性
func getHttpTransportHasPass(proxyUrl, user, pass string) *http.Transport {
	log.Printf("初始化带认证代理：%s, 用户名：%s", proxyUrl, user)

	// 读锁获取当前全局配置
	configLocker.RLock()
	cfg := globalConfig
	configLocker.RUnlock()

	// 补全代理URL前缀（如仅传IP:端口时补全http://）
	urli := url.URL{}
	if !strings.Contains(proxyUrl, "http") {
		proxyUrl = fmt.Sprintf("http://%s", proxyUrl)
	}

	// 解析代理URL并添加认证信息
	urlProxy, _ := urli.Parse(proxyUrl)
	if user != "" && pass != "" {
		urlProxy.User = url.UserPassword(user, pass)
	}

	// 创建带认证的Transport
	return newTransport(cfg, http.ProxyURL(urlProxy))
}

// NewGatherUtilHasPass 基础创建带认证代理的采集器实例
// 参数说明：
//
//	headers: 自定义请求头
//	proxyURL: 代理地址
//	user: 代理认证用户名
//	pass: 代理认证密码
//	timeOut: 采集超时时间（单位：秒）
//	isCookieLogOpen: Cookie变更时是否打印日志
//
// 使用示例：
//
//	headers := map[string]string{"User-Agent": "chrome"}
//	ga := NewGatherUtilHasPass(headers, "104.207.139.207:8080", "admin", "123456", 600, false)
func NewGatherUtilHasPass(headers map[string]string, proxyURL, user, pass string, timeOut int, isCookieLogOpen bool) *GatherStruct {
	var gather GatherStruct
	gather.Headers = make(map[string]string)

	// 自动补全默认请求头（同NewGatherUtil逻辑）
	if len(headers) == 1 {
		if v, exist := headers["User-Agent"]; exist {
			var defaultHeaders = make(map[string]string)
			defaultHeaders["Accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8"
			defaultHeaders["Accept-Encoding"] = "gzip, deflate, sdch"
			defaultHeaders["Accept-Language"] = "zh-CN,zh;q=0.8"
			defaultHeaders["Connection"] = "keep-alive"
			defaultHeaders["Upgrade-Insecure-Requests"] = "1"

			switch strings.ToLower(v) {
			case "baidu":
				defaultHeaders["User-Agent"] = "Mozilla/5.0 (compatible; Baiduspider/2.0;++http://www.baidu.com/search/spider.html)"
			case "google":
				defaultHeaders["User-Agent"] = "Mozilla/5.0 (compatible; Googlebot/2.1;+http://www.google.com/bot.html)"
			case "bing":
				defaultHeaders["User-Agent"] = "Mozilla/5.0 (compatible; bingbot/2.0;+http://www.bing.com/bingbot.htm)"
			case "chrome":
				defaultHeaders["User-Agent"] = "Mozilla/5.0 (Windows NT 6.1; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/56.0.2924.87 Safari/537.36"
			case "360":
				defaultHeaders["User-Agent"] = "Mozilla/5.0 (Windows NT 6.1; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/45.0.2454.101 Safari/537.36"
			case "ie", "ie9":
				defaultHeaders["User-Agent"] = "Mozilla/5.0 (compatible; MSIE 9.0; Windows NT 6.1; Win64; x64) Trident/5.0)"
			case "":
				defaultHeaders["User-Agent"] = "Mozilla/5.0 (Windows NT 6.1; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/56.0.2924.87 Safari/537.36"
			default:
				defaultHeaders["User-Agent"] = v
			}
			gather.Headers = defaultHeaders
		} else {
			gather.Headers = headers
		}
	} else {
		gather.Headers = headers
	}

	// 初始化Cookie管理器和HTTP客户端
	gather.J = newWebCookieJar(isCookieLogOpen)
	gather.Client = &http.Client{Transport: getHttpTransportHasPass(proxyURL, user, pass), Jar: gather.J}
	gather.Client.Timeout = time.Duration(timeOut) * time.Second

	// 同步请求头到并发安全存储
	for k, v := range gather.Headers {
		gather.safeHeaders.Store(k, v)
	}
	return &gather
}
