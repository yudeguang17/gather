// Copyright 2020 ratelimit Author(https://github.com/yudeguang17/gather). All Rights Reserved.
//
// This Source Code Form is subject to the terms of the MIT License.
// If a copy of the MIT License was not distributed with this file,
// You can obtain one at https://github.com/yudeguang17/gather.
// 模拟浏览器进行数据采集包,可较方便的定义http头，同时全自动化处理cookies
package gather

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
)

// Ungzip 自动判断并解压GZIP数据
// 逻辑：是标准GZIP则解压，否则直接返回原数据，无任何打印，仅解压失败返回原错误
func Ungzip(data []byte) (string, error) {
	// 空数据直接返回
	if len(data) == 0 {
		return "", nil
	}
	// 通过标准GZIP魔数0x1F8B判断，不依赖错误信息，可靠无歧义
	if len(data) < 2 || data[0] != 0x1F || data[1] != 0x8B {
		return string(data), nil
	}
	// 是GZIP，执行解压
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	defer reader.Close()

	uncompressedData, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(uncompressedData), nil
}

// newHttpRequest 构建HTTP请求，安全加载Header，不污染全局，防御类型异常
func (g *GatherStruct) newHttpRequest(method, URL, refererURL, cookies string, body io.Reader) (*http.Request, error) {
	// 核心实例空值校验，直接panic暴露严重问题
	if g == nil {
		panic("FATAL: GatherStruct 未初始化，请先通过 NewGather 系列函数创建")
	}
	if g.Client == nil {
		panic("FATAL: GatherStruct.Client 未初始化，实例创建异常")
	}

	// 创建请求，直接返回标准库原始错误
	req, err := http.NewRequest(method, URL, body)
	if err != nil {
		return nil, err
	}

	// 构建本次请求的临时Header，不修改全局safeHeaders
	requestHeaders := make(http.Header)
	// 安全遍历全局Header，类型断言+空值过滤，避免panic和无效Header
	g.safeHeaders.Range(func(k, v interface{}) bool {
		key, keyOk := k.(string)
		val, valOk := v.(string)
		if keyOk && valOk && key != "" && val != "" {
			requestHeaders.Set(key, val)
		}
		return true
	})

	// 临时设置Referer，仅本次请求生效
	if refererURL != "" {
		requestHeaders.Set("Referer", refererURL)
	}
	// 临时设置Cookie，仅本次请求生效
	if cookies != "" {
		requestHeaders.Set("Cookie", cookies)
	}

	req.Header = requestHeaders
	return req, nil
}

// request 执行HTTP请求，自动解压GZIP，无多余打印，错误直接返回
func (g *GatherStruct) request(req *http.Request) (html, redirectURL string, err error) {
	// 核心参数空值校验
	if req == nil {
		panic("FATAL: HTTP请求对象为nil，无法执行请求")
	}
	if g == nil || g.Client == nil {
		panic("FATAL: GatherStruct/Client 未初始化，无法执行请求")
	}

	// 执行请求，直接返回原始错误
	resp, err := g.Client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	// 非2xx状态码，返回自定义状态错误（无原始错误可返回）
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", http.ErrAbortHandler
	}

	// 读取响应体，直接返回原始错误
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}

	// 静默解压，失败则直接使用原始数据，无任何日志打印
	html, _ = Ungzip(respBody)
	// 获取最终跳转后的URL
	redirectURL = resp.Request.URL.String()

	return html, redirectURL, nil
}
