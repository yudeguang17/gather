// Copyright 2020 ratelimit Author(https://github.com/yudeguang17/gather). All Rights Reserved.
//
// This Source Code Form is subject to the terms of the MIT License.
// If a copy of the MIT was not distributed with this file,
// You can obtain one at https://github.com/yudeguang17/gather.
// 模拟浏览器进行数据采集包,可较方便的定义http头，同时全自动化处理cookies
package gather

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Ungzip 解压GZIP格式的字节数据
// 功能：
//  1. 解压gzip压缩的字节数组，返回字符串
//  2. 若输入非gzip格式（如普通文本），返回原数据+非错误信息
//
// 参数：
//
//	data: 待解压的字节数据
//
// 返回值：
//
//	string: 解压后的字符串（或原始字符串，若非gzip）
//	error: 仅在真实解压失败时返回（如数据损坏），非gzip格式返回nil
func Ungzip(data []byte) (string, error) {
	// 空数据直接返回
	if len(data) == 0 {
		return "", nil
	}

	// 创建gzip读取器
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		// 非gzip格式（最常见场景），返回原始数据+nil
		if strings.Contains(err.Error(), "gzip: invalid header") {
			return string(data), nil
		}
		// 真实解压错误（如数据损坏），返回错误
		return "", fmt.Errorf("gzip解压失败: %w", err)
	}
	// 确保reader关闭（即使ReadAll失败）
	defer func() {
		_ = reader.Close()
	}()

	// 替换废弃的ioutil.ReadAll为io.ReadAll（Go 1.16+标准）
	uncompressedData, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("读取gzip数据失败: %w", err)
	}

	return string(uncompressedData), nil
}

// newHttpRequest 创建HTTP请求对象（封装请求头、Cookie、Referer等）
// 核心原则：核心实例未初始化时直接panic（避免后续隐蔽错误），其他场景返回error
// 参数：
//
//	method: HTTP方法（GET/POST等）
//	URL: 请求地址
//	refererURL: 来源页URL（临时设置，不污染全局safeHeaders）
//	cookies: 本次请求的Cookie字符串（临时设置，不污染全局）
//	body: 请求体（GET为nil，POST为bytes.Reader等）
//
// 返回值：
//
//	*http.Request: 构建好的请求对象
//	error: 构建失败时返回（如URL无效、类型断言失败等）
func (g *GatherStruct) newHttpRequest(method, URL, refererURL, cookies string, body io.Reader) (*http.Request, error) {
	// 核心实例未初始化：直接panic（符合你的诉求，提前暴露严重问题）
	if g == nil {
		panic("FATAL: GatherStruct实例未初始化！请先通过NewGather/NewGatherUtil/NewGatherProxy函数创建实例后再调用")
	}
	if g.Client == nil {
		panic("FATAL: GatherStruct.Client未初始化！实例创建异常，请检查NewGather系列函数的实现")
	}

	// 创建基础请求对象
	req, err := http.NewRequest(method, URL, body)
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %w", err)
	}

	// 临时构建本次请求的Header（避免污染全局safeHeaders）
	requestHeaders := make(http.Header)

	// 1. 加载全局safeHeaders中的请求头（带类型断言安全检查）
	g.safeHeaders.Range(func(k, v interface{}) bool {
		key, ok1 := k.(string)
		value, ok2 := v.(string)
		if ok1 && ok2 && key != "" && value != "" {
			requestHeaders.Set(key, value)
		}
		return true
	})

	// 2. 临时设置Referer（仅本次请求有效，不修改全局）
	if refererURL != "" {
		requestHeaders.Set("Referer", refererURL)
	}

	// 3. 临时设置Cookie（仅本次请求有效，不修改全局）
	if cookies != "" {
		requestHeaders.Set("Cookie", cookies)
	}

	// 移除无意义的Header排序（HTTP协议不要求Header顺序）
	req.Header = requestHeaders

	return req, nil
}

// request 执行HTTP请求并处理响应（核心执行逻辑）
// 核心原则：仅在核心依赖缺失时panic，其他异常返回error
// 参数：
//
//	req: 已构建的HTTP请求对象
//
// 返回值：
//
//	html: 响应体字符串（自动解压GZIP）
//	redirectURL: 最终访问的URL（处理跳转后）
//	error: 请求失败/状态码异常时返回
func (g *GatherStruct) request(req *http.Request) (html, redirectURL string, err error) {
	// 核心参数缺失：直接panic（避免后续无效处理）
	if req == nil {
		panic("FATAL: 请求对象req为nil！请先通过newHttpRequest构建有效的请求对象")
	}
	if g == nil || g.Client == nil {
		panic("FATAL: GatherStruct/Client未初始化！请先调用NewGather系列函数")
	}

	// 执行请求
	resp, err := g.Client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("执行HTTP请求失败: %w", err)
	}

	// 安全关闭响应体（必须放在resp非nil分支，避免nil panic）
	defer func() {
		_ = resp.Body.Close()
	}()

	// 兼容所有2xx成功状态码（原仅支持200/202，过于严格）
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("HTTP请求失败，状态码: %d", resp.StatusCode)
	}

	// 读取响应体（替换废弃的ioutil.ReadAll）
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("读取响应体失败: %w", err)
	}

	// 自动解压GZIP（兼容非gzip格式）
	html, err = Ungzip(respBody)
	if err != nil {
		// 仅记录警告，仍返回原始数据（避免因解压错误丢失内容）
		fmt.Printf("警告：GZIP解压异常，返回原始数据: %v\n", err)
		html = string(respBody)
	}

	// 获取最终跳转后的URL（无跳转则为原URL）
	redirectURL = resp.Request.URL.String()

	return html, redirectURL, nil
}
