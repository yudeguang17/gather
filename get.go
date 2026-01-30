// Copyright 2020 ratelimit Author(https://github.com/yudeguang17/gather). All Rights Reserved.
//
// This Source Code Form is subject to the terms of the MIT License.
// If a copy of the MIT was not distributed with this file,
// You can obtain one at https://github.com/yudeguang17/gather.
// 模拟浏览器进行数据采集包,可较方便的定义http头，同时全自动化处理cookies
package gather

// Get 基于GET方法采集数据（自动复用实例内置Cookie）
// 功能：
//  1. 自动继承实例先前的Cookie（无需手动传入）
//  2. 自动处理301/302跳转，返回最终实际访问的URL
//  3. 线程安全：通过实例锁保证并发调用时的Cookie/请求头安全
//
// 参数：
//
//	URL: 待采集的目标URL（必填，如https://www.baidu.com/）
//	refererURL: 来源页URL（可选，空值则不设置Referer请求头）
//
// 返回值：
//
//	html: 目标页面的HTML文本内容（请求成功时返回）
//	redirectURL: 最终实际访问的URL（处理完所有跳转后的地址）
//	err: 错误信息（URL无效、网络异常、请求超时等场景返回非nil）
//
// 示例：
//
//	ga := NewGather("chrome", false)
//	html, redirectURL, err := ga.Get("https://www.baidu.com/", "")
//	if err != nil {
//	    log.Printf("GET请求失败: %v", err)
//	    return
//	}
//	fmt.Printf("最终访问地址: %s, 页面内容长度: %d", redirectURL, len(html))
func (g *GatherStruct) Get(URL, refererURL string) (html, redirectURL string, err error) {
	return g.GetUtil(URL, refererURL, "")
}

// GetUtil 基于GET方法采集数据（支持手动指定Cookie）
// 功能：
//  1. 手动传入Cookie时，优先使用传入的Cookie（覆盖实例内置Cookie）
//  2. Cookie留空时，自动继承实例先前的Cookie（同Get方法）
//  3. 自动处理跳转，返回最终访问URL，线程安全
//
// 参数：
//
//	URL: 待采集的目标URL（必填）
//	refererURL: 来源页URL（可选，空值不设置Referer）
//	cookies: 手动指定的Cookie字符串（可选，格式同浏览器Cookie，如"key1=val1; key2=val2"）
//
// 返回值：
//
//	html: 目标页面HTML内容（请求成功时返回）
//	redirectURL: 最终实际访问的URL（跳转后的地址）
//	err: 错误信息（URL解析失败、网络错误、请求超时等）
//
// 注意事项：
//  1. 手动传入的Cookie优先级高于实例内置Cookie，适用于登录态指定场景
//  2. 函数内通过g.locker加锁，保证并发调用时请求头/Cookie不被篡改
//
// 示例：
//
//	ga := NewGather("chrome", false)
//	// 手动传入登录态Cookie（修正原示例参数顺序错误）
//	cookies := `SINAGLOBAL=8868584542946.604.1509350660873; YF-Page-G0=b9385a03a044baf8db46b84f3ff125a0`
//	html, redirectURL, err := ga.GetUtil("https://weibo.com/xxxxxx", "", cookies)
//	if err != nil {
//	    log.Printf("GET请求失败: %v", err)
//	    return
//	}
func (g *GatherStruct) GetUtil(URL, refererURL, cookies string) (html, redirectURL string, err error) {
	g.locker.Lock()
	defer g.locker.Unlock()
	req, err := g.newHttpRequest("GET", URL, refererURL, cookies, nil)
	if err != nil {
		return "", "", err
	}
	return g.request(req)
}
