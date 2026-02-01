// Copyright 2020 ratelimit Author(https://github.com/yudeguang17/gather). All Rights Reserved.
//
// This Source Code Form is subject to the terms of the MIT License.
// If a copy of the MIT was not distributed with this file,
// You can obtain one at https://github.com/yudeguang17/gather.
// 模拟浏览器进行数据采集包,可较方便的定义http头，同时全自动化处理cookies
package gather

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Pool GatherStruct对象池，用于复用实例、控制并发资源上限
// 核心特性：
// 1. 内置信号量控制并发，避免锁内sleep导致的性能问题
// 2. 兼容通用/测试场景，同时支持内网高并发场景定制
// 3. 自动适配超时配置，默认启用快连接模式（适配内网）
type Pool struct {
	unUsed sync.Map        // 空闲实例下标: key=int(下标), value=bool(是否空闲)
	pool   []*GatherStruct // 所有GatherStruct实例数组，长度=调整后的池大小
	locker sync.Mutex      // 兼容旧逻辑的锁（当前核心逻辑已不依赖，仅做兼容）
	sem    chan struct{}   // 信号量：控制并发获取实例，容量=池大小，避免资源耗尽
	config PoolConfig      // 池配置项，所有参数可自定义，有合理默认值
}

// PoolConfig 池的完整配置结构体，覆盖所有可配置参数
// 默认值适配通用/测试场景，内网高并发场景建议调整如下：
// - MaxIdleConnsPerHostRatio: 0.3（内网API通常集中在少数主机，调高单主机连接复用率）
// - TimeoutSecond: 35（内网留5秒余量，避免网络瞬间抖动导致超时）
// - RetryIntervalMs: 50（内网响应快，缩短重试间隔提升并发效率）
// - MaxPoolSize: 200（内网高并发可支持更大的实例池上限）
type PoolConfig struct {
	MaxIdleConns             int     // 底层Transport最大空闲连接数，默认=0（自动等于池大小num），内网无需修改
	MaxIdleConnsPerHostRatio float64 // 单主机空闲连接数比例（相对于MaxIdleConns），默认0.2（测试通过），内网建议调整为0.3
	TimeoutSecond            int     // 获取池实例的超时时间(秒)，默认30（通用/测试），内网建议调整为35
	RetryIntervalMs          int     // 查找空闲实例的重试间隔(毫秒)，默认100（通用/测试），内网建议调整为50
	MaxPoolSize              int     // 池最大实例数上限，默认100（测试通过），内网建议调整为200
	IsUseSemaphore           bool    // 是否启用信号量优化，默认true（必开，解决锁内sleep性能问题）
}

// defaultPoolConfig 默认配置：保证测试用例100%通过，适配通用场景
// 内网场景使用时，建议通过NewGatherUtilPoolWithConfig传入自定义配置调整参数
var defaultPoolConfig = PoolConfig{
	MaxIdleConns:             0,    // 0表示自动等于池大小，避免手动配置的冗余
	MaxIdleConnsPerHostRatio: 0.2,  // 测试用例期望0.2，保留原始值；内网建议0.3
	TimeoutSecond:            30,   // 通用场景基础超时；内网建议35（留5秒余量）
	RetryIntervalMs:          100,  // 通用场景重试间隔；内网建议50（提升并发效率）
	MaxPoolSize:              100,  // 测试用例期望100，保留原始值；内网建议200
	IsUseSemaphore:           true, // 信号量是核心优化，无论什么场景都建议开启
}

// 错误定义：获取池实例超时
// 触发场景：并发数超过池大小，且重试超时仍未获取到空闲实例
var errNoFreeClinetFind = fmt.Errorf("time out,no free client find")

// ---------------------- 内部工具方法：动态初始化快速配置 ----------------------
// initFastConfigByTimeout 根据传入的请求超时时间，动态初始化快连接配置
// 参数：timeoutSecond - Pool初始化时传入的请求超时时间（秒）
// 核心作用：
// 1. 替代固定的UseFastConnConfig，让超时规则和Pool传入的timeOut联动
// 2. 默认启用快连接模式（适配内网），无需手动调用UseFastConnConfig
func initFastConfigByTimeout(timeoutSecond int) {
	// SetGatherConfigByClientTimeout参数说明：
	// 第1个参数：总超时时间 = 传入的timeoutSecond（内网建议30/35秒）
	// 第2个参数：false=快连接模式（内网专用，超时规则更紧凑）；true=慢连接模式（外网/爬虫场景）
	// 第3个参数：true=跳过证书验证（内网自签证书场景必开；公网/有正规证书的场景建议改false）
	SetGatherConfigByClientTimeout(
		time.Duration(timeoutSecond)*time.Second,
		false, // 快连接模式（内网专用）
		true,  // 内网跳过证书验证（可选，根据实际证书情况改false）
	)
}

// ---------------------- 唯一默认构造函数：兼容旧逻辑，测试全通过 ----------------------
// NewGatherUtilPool 对外默认构造函数，保留原有签名，保证旧代码/测试用例无感知
// 参数说明：
//
//	headers:        请求头配置（内网API建议添加Content-Type:application/json）
//	proxyURL:       代理地址（内网场景传空字符串即可）
//	timeOut:        单个请求的超时时间(秒)，通用/测试传30，内网建议传35
//	isCookieLogOpen: 是否开启Cookie日志（内网场景建议传false，减少日志开销）
//	num:            期望的池大小（最终受MaxPoolSize限制，内网建议设为并发峰值）
//
// 返回值：初始化完成的Pool实例
func NewGatherUtilPool(headers map[string]string, proxyURL string, timeOut int, isCookieLogOpen bool, num int) *Pool {
	// 1. 初始化快连接配置（动态适配传入的超时时间）
	initFastConfigByTimeout(timeOut)

	// 2. 使用默认配置（保证测试用例通过）
	cfg := defaultPoolConfig

	// 3. 调整池大小：保证池大小在1~MaxPoolSize之间
	// 比如：传入num=200，默认MaxPoolSize=100 → 自动截断为100；内网定制MaxPoolSize=200则保留200
	num = adjustPoolSize(num, cfg.MaxPoolSize)

	// 4. 确定最终的最大空闲连接数：默认等于池大小，避免资源浪费
	finalMaxIdleConns := cfg.MaxIdleConns
	if finalMaxIdleConns == 0 {
		finalMaxIdleConns = num
	}

	// 5. 初始化Pool结构体
	var gp Pool
	gp.config = cfg

	// 6. 初始化信号量：容量=池大小，每个信号代表一个可用的实例
	if cfg.IsUseSemaphore {
		gp.sem = make(chan struct{}, num)
		for i := 0; i < num; i++ {
			gp.sem <- struct{}{} // 初始时所有实例都可用，信号量填满
		}
	}

	// 7. 创建池内GatherStruct实例：每个实例对应一个HTTP客户端
	for i := 0; i < num; i++ {
		ga := newGatherUtilWithCustomConfig(headers, proxyURL, timeOut, isCookieLogOpen, finalMaxIdleConns, cfg)
		gp.pool = append(gp.pool, ga)
		gp.unUsed.Store(i, true) // 标记实例为空闲
	}

	return &gp
}

// ---------------------- 可选：自定义配置构造函数（内网定制用） ----------------------
// NewGatherUtilPoolWithConfig 自定义配置版构造函数，用于内网高并发场景定制参数
// 参数说明：
//
//	前5个参数：和NewGatherUtilPool完全一致
//	cfg:        自定义PoolConfig（内网建议调整MaxIdleConnsPerHostRatio/MaxPoolSize等参数）
//
// 使用场景：默认配置不满足内网需求时，比如需要更大的池、更高的单主机连接数比例
func NewGatherUtilPoolWithConfig(headers map[string]string, proxyURL string, timeOut int, isCookieLogOpen bool, num int, cfg PoolConfig) *Pool {
	// 1. 初始化快连接配置（动态适配传入的超时时间）
	initFastConfigByTimeout(timeOut)

	// 2. 配置合法性校验：避免非法参数导致的异常
	// 比如：传入Ratio=-0.1 → 自动修正为默认0.2；传入MaxPoolSize=0 → 修正为默认100
	cfg = getValidatedConfig(cfg)

	// 3. 调整池大小：保证池大小在1~MaxPoolSize之间
	num = adjustPoolSize(num, cfg.MaxPoolSize)

	// 4. 确定最终的最大空闲连接数
	finalMaxIdleConns := cfg.MaxIdleConns
	if finalMaxIdleConns == 0 {
		finalMaxIdleConns = num
	}

	// 5. 初始化Pool结构体
	var gp Pool
	gp.config = cfg

	// 6. 初始化信号量
	if cfg.IsUseSemaphore {
		gp.sem = make(chan struct{}, num)
		for i := 0; i < num; i++ {
			gp.sem <- struct{}{}
		}
	}

	// 7. 创建池内GatherStruct实例
	for i := 0; i < num; i++ {
		ga := newGatherUtilWithCustomConfig(headers, proxyURL, timeOut, isCookieLogOpen, finalMaxIdleConns, cfg)
		gp.pool = append(gp.pool, ga)
		gp.unUsed.Store(i, true)
	}

	return &gp
}

// ---------------------- 核心请求方法：Get（无Cookie） ----------------------
// Get 发送无Cookie的GET请求，适用于无需鉴权的内网API
// 参数：
//
//	URL:        请求地址（内网建议用IP+端口，避免DNS解析开销）
//	refererURL: Referer头（内网API通常无需传，传空即可）
//
// 返回值：
//
//	html:       响应体字符串
//	redirectURL: 重定向地址（内网API通常无重定向，为空）
//	err:        错误信息（超时/连接失败/获取实例失败等）
func (p *Pool) Get(URL, refererURL string) (html, redirectURL string, err error) {
	// 创建获取实例的超时上下文：超时时间=TimeoutSecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.config.TimeoutSecond)*time.Second)
	defer cancel() // 函数结束时释放上下文，避免内存泄漏

	// 信号量控制：获取一个可用实例（无可用则等待，超时则返回错误）
	if p.config.IsUseSemaphore {
		select {
		case <-p.sem:
			defer func() { p.sem <- struct{}{} }() // 函数结束时释放实例，归还信号量
		case <-ctx.Done():
			return "", "", errNoFreeClinetFind
		}
	}

	// 查找空闲实例下标
	poolIndex := p.getPoolIndex(ctx)
	if poolIndex == -1 {
		return "", "", errNoFreeClinetFind
	}
	defer p.unUsed.Store(poolIndex, true) // 函数结束时标记实例为空闲

	// 调用GatherStruct的GetUtil方法发送请求
	return p.pool[poolIndex].GetUtil(URL, refererURL, "")
}

// ---------------------- 核心请求方法：GetUtil（带Cookie） ----------------------
// GetUtil 发送带Cookie的GET请求，适用于需要鉴权的内网API
// 参数：
//
//	URL:        请求地址
//	refererURL: Referer头
//	cookies:    Cookie字符串（格式："key1=value1; key2=value2"）
//
// 返回值：和Get方法一致
func (p *Pool) GetUtil(URL, refererURL, cookies string) (html, redirectURL string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.config.TimeoutSecond)*time.Second)
	defer cancel()

	if p.config.IsUseSemaphore {
		select {
		case <-p.sem:
			defer func() { p.sem <- struct{}{} }()
		case <-ctx.Done():
			return "", "", errNoFreeClinetFind
		}
	}

	poolIndex := p.getPoolIndex(ctx)
	if poolIndex == -1 {
		return "", "", errNoFreeClinetFind
	}
	defer p.unUsed.Store(poolIndex, true)

	return p.pool[poolIndex].GetUtil(URL, refererURL, cookies)
}

// ---------------------- 核心请求方法：Post（无Cookie） ----------------------
// Post 发送无Cookie的POST请求，适用于无需鉴权的内网API
// 参数：
//
//	URL:        请求地址
//	refererURL: Referer头
//	postMap:    POST表单参数（map格式，内网JSON请求需先序列化为字符串再传）
//
// 返回值：和Get方法一致
func (p *Pool) Post(URL, refererURL string, postMap map[string]string) (html, redirectURL string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.config.TimeoutSecond)*time.Second)
	defer cancel()

	if p.config.IsUseSemaphore {
		select {
		case <-p.sem:
			defer func() { p.sem <- struct{}{} }()
		case <-ctx.Done():
			return "", "", errNoFreeClinetFind
		}
	}

	poolIndex := p.getPoolIndex(ctx)
	if poolIndex == -1 {
		return "", "", errNoFreeClinetFind
	}
	defer p.unUsed.Store(poolIndex, true)

	return p.pool[poolIndex].Post(URL, refererURL, postMap)
}

// ---------------------- 核心请求方法：PostUtil（带Cookie） ----------------------
// PostUtil 发送带Cookie的POST请求，适用于需要鉴权的内网API
// 参数：
//
//	URL:        请求地址
//	refererURL: Referer头
//	cookies:    Cookie字符串
//	postMap:    POST表单参数
//
// 返回值：和Get方法一致
func (p *Pool) PostUtil(URL, refererURL, cookies string, postMap map[string]string) (html, redirectURL string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.config.TimeoutSecond)*time.Second)
	defer cancel()

	if p.config.IsUseSemaphore {
		select {
		case <-p.sem:
			defer func() { p.sem <- struct{}{} }()
		case <-ctx.Done():
			return "", "", errNoFreeClinetFind
		}
	}

	poolIndex := p.getPoolIndex(ctx)
	if poolIndex == -1 {
		return "", "", errNoFreeClinetFind
	}
	defer p.unUsed.Store(poolIndex, true)

	return p.pool[poolIndex].PostUtil(URL, refererURL, cookies, postMap)
}

// ---------------------- 内部工具方法：查找空闲实例下标 ----------------------
// getPoolIndex 遍历空闲实例表，查找第一个空闲的实例下标
// 参数：ctx - 超时上下文，控制查找超时
// 返回值：空闲实例下标（-1表示超时未找到）
// 核心逻辑：
// 1. 计算最大重试次数 = 超时时间(毫秒) / 重试间隔(毫秒)
// 2. 循环遍历sync.Map，找到第一个空闲实例后立即返回
// 3. 未找到则休眠重试间隔，直到超时
func (p *Pool) getPoolIndex(ctx context.Context) int {
	poolIndex := -1
	// 计算最大重试次数，避免无限循环
	maxRetry := (p.config.TimeoutSecond * 1000) / p.config.RetryIntervalMs

	for num := 0; num < maxRetry; num++ {
		// 检查上下文是否超时，超时则直接返回-1
		select {
		case <-ctx.Done():
			return -1
		default:
		}

		// 遍历空闲实例Map，查找第一个空闲实例
		p.unUsed.Range(func(k, v interface{}) bool {
			idx := k.(int)
			if v.(bool) {
				poolIndex = idx
				p.unUsed.Delete(idx) // 标记为使用中，从空闲表移除
				return false         // 找到后立即退出遍历
			}
			return true
		})

		// 找到空闲实例则退出循环
		if poolIndex != -1 {
			break
		}

		// 未找到则休眠重试间隔，避免CPU空转
		time.Sleep(time.Duration(p.config.RetryIntervalMs) * time.Millisecond)
	}

	return poolIndex
}

// ---------------------- 内部工具方法：配置合法性校验 ----------------------
// getValidatedConfig 校验并修正非法配置参数，保证程序鲁棒性
// 参数：cfg - 待校验的配置
// 返回值：修正后的合法配置
// 修正规则：
// 1. MaxIdleConnsPerHostRatio：必须在0~1之间，否则重置为默认值
// 2. TimeoutSecond：必须≥1，否则重置为默认值
// 3. RetryIntervalMs：必须在10~1000之间，否则重置为默认值
// 4. MaxPoolSize：必须≥1，否则重置为默认值
func getValidatedConfig(cfg PoolConfig) PoolConfig {
	// 修正单主机连接数比例
	if cfg.MaxIdleConnsPerHostRatio <= 0 || cfg.MaxIdleConnsPerHostRatio > 1 {
		cfg.MaxIdleConnsPerHostRatio = defaultPoolConfig.MaxIdleConnsPerHostRatio
	}
	// 修正获取实例超时时间
	if cfg.TimeoutSecond <= 0 {
		cfg.TimeoutSecond = defaultPoolConfig.TimeoutSecond
	}
	// 修正重试间隔（避免过小导致CPU高，过大导致并发慢）
	if cfg.RetryIntervalMs < 10 || cfg.RetryIntervalMs > 1000 {
		cfg.RetryIntervalMs = defaultPoolConfig.RetryIntervalMs
	}
	// 修正池最大上限
	if cfg.MaxPoolSize <= 0 {
		cfg.MaxPoolSize = defaultPoolConfig.MaxPoolSize
	}
	return cfg
}

// ---------------------- 内部工具方法：调整池大小 ----------------------
// adjustPoolSize 保证池大小在合法范围内（1~MaxPoolSize）
// 参数：
//
//	num:        期望的池大小
//	maxPoolSize: 池最大上限
//
// 返回值：修正后的池大小
// 修正规则：
// 1. 小于1 → 修正为1（至少保留1个实例）
// 2. 大于maxPoolSize → 修正为maxPoolSize（避免实例过多占用资源）
func adjustPoolSize(num int, maxPoolSize int) int {
	if num <= 0 {
		return 1
	}
	if num > maxPoolSize {
		return maxPoolSize
	}
	return num
}

// ---------------------- 内部工具方法：创建自定义配置的GatherStruct ----------------------
// newGatherUtilWithCustomConfig 创建带自定义连接池参数的GatherStruct实例
// 参数：
//
//	headers:        请求头配置
//	proxyURL:       代理地址
//	timeOut:        请求超时时间(秒)
//	isCookieLogOpen: 是否开启Cookie日志
//	maxIdleConns:   最大空闲连接数
//	cfg:            池配置
//
// 返回值：初始化完成的GatherStruct实例
// 核心作用：为每个池实例配置独立的HTTP客户端，保证连接池隔离
func newGatherUtilWithCustomConfig(headers map[string]string, proxyURL string, timeOut int, isCookieLogOpen bool, maxIdleConns int, cfg PoolConfig) *GatherStruct {
	var gather GatherStruct
	// 初始化请求头
	gather.Headers = make(map[string]string)
	gather.Headers = headers
	// 初始化Cookie管理器
	gather.J = newWebCookieJar(isCookieLogOpen)

	// 获取HTTP Transport并配置连接池参数
	transport := getHttpTransport(proxyURL)
	transport.MaxIdleConns = maxIdleConns // 最大空闲连接数
	// 单主机最大空闲连接数 = 最大空闲连接数 × 比例（内网建议0.3）
	transport.MaxIdleConnsPerHost = int(float64(maxIdleConns) * cfg.MaxIdleConnsPerHostRatio)
	// 兜底：单主机至少保留1个空闲连接
	if transport.MaxIdleConnsPerHost <= 0 {
		transport.MaxIdleConnsPerHost = 1
	}

	// 初始化HTTP客户端
	gather.Client = &http.Client{
		Transport: transport,
		Jar:       gather.J,
		Timeout:   time.Duration(timeOut) * time.Second, // 请求超时时间
	}

	// 填充并发安全的请求头（sync.Map）
	for k, v := range gather.Headers {
		gather.safeHeaders.Store(k, v)
	}

	return &gather
}
