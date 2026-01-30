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
// 核心设计：兼容旧调用 + 可配置化 + 资源可控
type Pool struct {
	unUsed sync.Map        // 空闲实例下标: key=int(下标), value=bool(是否空闲)
	pool   []*GatherStruct // 所有GatherStruct实例数组
	locker sync.Mutex      // 兼容旧逻辑的锁（当前核心逻辑不依赖）
	sem    chan struct{}   // 信号量：控制并发获取实例（替代锁内sleep）
	config PoolConfig      // 池配置项（全可自定义，有合理默认值）
}

// PoolConfig 池的完整配置结构体，所有硬编码参数均纳入配置
// 所有字段有默认值，未自定义时与原有逻辑兼容
type PoolConfig struct {
	MaxIdleConns             int     // 底层Transport最大空闲连接数，默认=池大小(num)
	MaxIdleConnsPerHostRatio float64 // 单主机最大空闲连接数比例（相对于MaxIdleConns），默认0.2
	TimeoutSecond            int     // 获取池实例的超时时间(秒)，默认30
	RetryIntervalMs          int     // 查找空闲实例的重试间隔(毫秒)，默认100
	MaxPoolSize              int     // 池最大实例数上限，默认100
	IsUseSemaphore           bool    // 是否启用信号量优化（默认true）
}

// defaultPoolConfig 默认配置：兼容原有逻辑 + 动态适配
var defaultPoolConfig = PoolConfig{
	MaxIdleConns:             0,    // 0表示自动等于池大小(num)，避免资源浪费
	MaxIdleConnsPerHostRatio: 0.2,  // 单主机空闲连接数=MaxIdleConns×0.2（替代原固定/5）
	TimeoutSecond:            30,   // 通用超时时间，兼顾内网/外网场景
	RetryIntervalMs:          100,  // 重试间隔，平衡CPU占用与响应速度
	MaxPoolSize:              100,  // 池大小默认上限，避免创建过多实例
	IsUseSemaphore:           true, // 默认启用信号量，解决锁内sleep性能问题
}

// 错误定义：获取池实例超时
var errNoFreeClinetFind = fmt.Errorf("time out,no free client find")

// ---------------------- 1. 完全兼容原有调用的构造函数 ----------------------
// NewGatherUtilPool 保留原有签名，旧代码无需任何修改
func NewGatherUtilPool(headers map[string]string, proxyURL string, timeOut int, isCookieLogOpen bool, num int) *Pool {
	return NewGatherUtilPoolWithConfig(headers, proxyURL, timeOut, isCookieLogOpen, num, defaultPoolConfig)
}

// ---------------------- 2. 带自定义配置的核心构造函数 ----------------------
// NewGatherUtilPoolWithConfig 池的核心构造函数，支持全配置自定义
// 参数说明：
//
//	headers: 请求头配置
//	proxyURL: 代理地址
//	timeOut: GatherStruct请求超时时间(秒)
//	isCookieLogOpen: 是否开启Cookie日志
//	num: 期望的池大小（最终受MaxPoolSize限制）
//	config: 可选配置（可变参数），未传则用默认值
func NewGatherUtilPoolWithConfig(headers map[string]string, proxyURL string, timeOut int, isCookieLogOpen bool, num int, config ...PoolConfig) *Pool {
	// 处理配置默认值
	cfg := defaultPoolConfig
	if len(config) > 0 {
		cfg = config[0]
		// 配置合法性兜底：避免非法值导致异常
		cfg = getValidatedConfig(cfg)
	}

	// 调整池大小：基于MaxPoolSize限制，且保证≥1
	num = adjustPoolSize(num, cfg.MaxPoolSize)

	// 确定最终的MaxIdleConns：0则自动等于池大小
	finalMaxIdleConns := cfg.MaxIdleConns
	if finalMaxIdleConns == 0 {
		finalMaxIdleConns = num
	}

	// 初始化Pool
	var gp Pool
	gp.config = cfg

	// 初始化信号量（启用时）
	if cfg.IsUseSemaphore {
		gp.sem = make(chan struct{}, num)
		for i := 0; i < num; i++ {
			gp.sem <- struct{}{}
		}
	}

	// 创建池内GatherStruct实例
	for i := 0; i < num; i++ {
		ga := newGatherUtilWithCustomConfig(headers, proxyURL, timeOut, isCookieLogOpen, finalMaxIdleConns, cfg)
		gp.pool = append(gp.pool, ga)
		gp.unUsed.Store(i, true) // 标记为空闲
	}

	return &gp
}

// ---------------------- 3. 核心请求方法：Get（无Cookie） ----------------------
func (p *Pool) Get(URL, refererURL string) (html, redirectURL string, err error) {
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

	return p.pool[poolIndex].GetUtil(URL, refererURL, "")
}

// ---------------------- 4. 核心请求方法：GetUtil（带Cookie） ----------------------
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

// ---------------------- 5. 核心请求方法：Post（无Cookie） ----------------------
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

// ---------------------- 6. 核心请求方法：PostUtil（带Cookie） ----------------------
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

// ---------------------- 7. 内部方法：获取空闲实例下标 ----------------------
// getPoolIndex 查找并占用空闲实例下标，支持上下文超时
func (p *Pool) getPoolIndex(ctx context.Context) int {
	poolIndex := -1
	// 计算最大重试次数：超时时间(秒) × 1000 / 重试间隔(毫秒)
	maxRetry := (p.config.TimeoutSecond * 1000) / p.config.RetryIntervalMs

	for num := 0; num < maxRetry; num++ {
		// 检查超时，提前退出
		select {
		case <-ctx.Done():
			return -1
		default:
		}

		// 遍历空闲表，找第一个空闲下标
		p.unUsed.Range(func(k, v interface{}) bool {
			idx := k.(int)
			if v.(bool) {
				poolIndex = idx
				p.unUsed.Delete(idx) // 标记为使用中
				return false
			}
			return true
		})

		if poolIndex != -1 {
			break
		}

		// 按配置的间隔休眠，避免CPU空转
		time.Sleep(time.Duration(p.config.RetryIntervalMs) * time.Millisecond)
	}

	return poolIndex
}

// ---------------------- 内部工具方法：配置合法性校验 ----------------------
// getValidatedConfig 校验配置，确保所有参数合法
func getValidatedConfig(cfg PoolConfig) PoolConfig {
	// MaxIdleConnsPerHostRatio：0~1之间，否则重置为默认0.2
	if cfg.MaxIdleConnsPerHostRatio <= 0 || cfg.MaxIdleConnsPerHostRatio > 1 {
		cfg.MaxIdleConnsPerHostRatio = defaultPoolConfig.MaxIdleConnsPerHostRatio
	}
	// TimeoutSecond：≥1，否则重置为30
	if cfg.TimeoutSecond <= 0 {
		cfg.TimeoutSecond = defaultPoolConfig.TimeoutSecond
	}
	// RetryIntervalMs：10~1000之间，否则重置为100
	if cfg.RetryIntervalMs < 10 || cfg.RetryIntervalMs > 1000 {
		cfg.RetryIntervalMs = defaultPoolConfig.RetryIntervalMs
	}
	// MaxPoolSize：≥1，否则重置为100
	if cfg.MaxPoolSize <= 0 {
		cfg.MaxPoolSize = defaultPoolConfig.MaxPoolSize
	}
	return cfg
}

// ---------------------- 内部工具方法：调整池大小 ----------------------
// adjustPoolSize 调整池大小，保证在1~MaxPoolSize之间
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
// newGatherUtilWithCustomConfig 创建带自定义连接池配置的GatherStruct
func newGatherUtilWithCustomConfig(headers map[string]string, proxyURL string, timeOut int, isCookieLogOpen bool, maxIdleConns int, cfg PoolConfig) *GatherStruct {
	var gather GatherStruct
	gather.Headers = make(map[string]string)
	gather.Headers = headers
	gather.J = newWebCookieJar(isCookieLogOpen)

	// 获取基础Transport并调整连接池参数
	transport := getHttpTransport(proxyURL)
	transport.MaxIdleConns = maxIdleConns
	// 单主机空闲连接数：基于比例计算（避免整数除精度丢失）
	transport.MaxIdleConnsPerHost = int(float64(maxIdleConns) * cfg.MaxIdleConnsPerHostRatio)
	// 兜底：单主机空闲连接数至少为1
	if transport.MaxIdleConnsPerHost <= 0 {
		transport.MaxIdleConnsPerHost = 1
	}

	// 初始化HTTP Client
	gather.Client = &http.Client{
		Transport: transport,
		Jar:       gather.J,
		Timeout:   time.Duration(timeOut) * time.Second,
	}

	// 填充并发安全的请求头
	for k, v := range gather.Headers {
		gather.safeHeaders.Store(k, v)
	}

	return &gather
}
