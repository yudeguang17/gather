// Copyright 2020 ratelimit Author(https://github.com/yudeguang17/gather). All Rights Reserved.
//
// This Source Code Form is subject to the terms of the MIT License.
// If a copy of the MIT was not distributed with this file,
// You can obtain one at https://github.com/yudeguang17/gather.
// 模拟浏览器进行数据采集包,可较方便的定义http头，同时全自动化处理cookies
package gather

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto" // 仅用于构建MIME Header，不再依赖Quote函数
	"net/url"
	"strings"
)

// -------------------------- 新增：自定义兼容版 Quote 函数（替代textproto.Quote） --------------------------
// quote 兼容所有Go版本的HTTP头参数转义函数，逻辑与Go 1.20+ textproto.Quote完全一致
// 作用：转义双引号、换行、回车等特殊字符，为字符串添加双引号，符合HTTP规范
func quote(s string) string {
	if len(s) == 0 {
		return `""`
	}
	// 检查是否需要转义
	needsEscape := false
	for _, c := range s {
		if c == '"' || c == '\r' || c == '\n' || c == '\\' {
			needsEscape = true
			break
		}
	}
	if !needsEscape {
		return fmt.Sprintf(`"%s"`, s)
	}
	// 转义特殊字符
	var buf bytes.Buffer
	buf.WriteByte('"')
	for _, c := range s {
		switch c {
		case '"', '\\':
			buf.WriteByte('\\')
			buf.WriteRune(c)
		case '\r':
			buf.WriteString(`\r`)
		case '\n':
			buf.WriteString(`\n`)
		default:
			buf.WriteRune(c)
		}
	}
	buf.WriteByte('"')
	return buf.String()
}

/*
post方式获取数据,自动继承先前的cookies
URL:指待抓取的URL
refererURL:上一次访问的URL。某些防抓取比较严格的网站会对上次访问的页面URL进行验证
redirectURL:最终实际访问到内容的URL。因为有时候会碰到301跳转等情况，最终访问的URL并非输入的URL
postMap:指post过去的相关数据

例:
ga:= NewGather("chrome", false)
postMap := make(map[string]string)
postMap["user"] = "ydg"
postMap["password"] = "abcdef"
html, redirectURL, err := ga.Post("https://weibo.com/xxxxx", "", postMap)
*/
func (g *GatherStruct) Post(URL, refererURL string, postMap map[string]string) (html, redirectURL string, err error) {
	return g.PostUtil(URL, refererURL, "", postMap)
}

/*
post方式获取数据,手动增加cookies
URL:指待抓取的URL
refererURL:上一次访问的URL。某些防抓取比较严格的网站会对上次访问的页面URL进行验证
redirectURL:最终实际访问到内容的URL。因为有时候会碰到301跳转等情况，最终访问的URL并非输入的URL
postMap:指post过去的相关数据
例:
ga := NewGather("chrome", false)
cookies := `SINAGLOBAL=8868584542946.604.1509350660873;??????????; YF-Page-G0=b9385a03a044baf8db46b84f3ff125a0`
postMap := make(map[string]string)
postMap["user"] = "ydg"
postMap["password"] = "abcdef"
html, redirectURL, err := ga.PostUtil("https://weibo.com/xxxxx", "",cookies, postMap)
*/
func (g *GatherStruct) PostUtil(URL, refererURL, cookies string, postMap map[string]string) (html, redirectURL string, err error) {
	g.locker.Lock()
	defer g.locker.Unlock()

	// 构建POST表单数据
	postValues := url.Values{}
	for k, v := range postMap {
		postValues.Set(k, v)
	}
	postDataBytes := []byte(postValues.Encode())
	postBytesReader := bytes.NewReader(postDataBytes)

	// 规范Content-Type：移除多余的param=value，补充utf-8
	if _, exist := g.safeHeaders.Load("Content-Type"); !exist {
		g.safeHeaders.Store("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	}

	req, err := g.newHttpRequest("POST", URL, refererURL, cookies, postBytesReader)
	if err != nil {
		return "", "", err
	}
	return g.request(req)
}

// PostUtilReq 构建POST请求对象（不执行请求）
func (g *GatherStruct) PostUtilReq(URL, refererURL, cookies string, postMap map[string]string) (*http.Request, error) {
	g.locker.Lock()
	defer g.locker.Unlock()

	postValues := url.Values{}
	for k, v := range postMap {
		postValues.Set(k, v)
	}
	postDataBytes := []byte(postValues.Encode())
	postBytesReader := bytes.NewReader(postDataBytes)

	// 规范Content-Type
	if _, exist := g.safeHeaders.Load("Content-Type"); !exist {
		g.safeHeaders.Store("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	}

	return g.newHttpRequest("POST", URL, refererURL, cookies, postBytesReader)
}

// POST二进制数据
// 补充说明：默认Content-Type为application/octet-stream，可通过safeHeaders自定义
func (g *GatherStruct) PostBytes(URL, refererURL, cookies string, postBytes []byte) (html, redirectURL string, err error) {
	g.locker.Lock()
	defer g.locker.Unlock()

	postBytesReader := bytes.NewReader(postBytes)

	// 为二进制POST设置默认Content-Type
	if _, exist := g.safeHeaders.Load("Content-Type"); !exist {
		g.safeHeaders.Store("Content-Type", "application/octet-stream")
	}

	req, err := g.newHttpRequest("POST", URL, refererURL, cookies, postBytesReader)
	if err != nil {
		return "", "", err
	}
	return g.request(req)
}

/*
以XML的方式post数据,自动继承先前的cookies
URL:指待抓取的URL
refererURL:上一次访问的URL。某些防抓取比较严格的网站会对上次访问的页面URL进行验证
redirectURL:最终实际访问到内容的URL。因为有时候会碰到301跳转等情况，最终访问的URL并非输入的URL
postXML:指待Post的XML数据，文本类型
例:
ga := gather.NewGather("chrome", false)
postXML := `<?xml version="1.0" encoding="utf-8"?><login><user>ydg</user><password>abcdef</password></login>`
html, redirectURL, err := ga.PostXML(`https://weibo.com/xxxxx`, "", postXML)
*/
func (g *GatherStruct) PostXML(URL, refererURL, postXML string) (html, redirectURL string, err error) {
	return g.PostXMLUtil(URL, refererURL, "", postXML)
}

/*
以XML的方式post数据,手动增加cookies
URL:指待抓取的URL
refererURL:上一次访问的URL。某些防抓取比较严格的网站会对上次访问的页面URL进行验证
redirectURL:最终实际访问到内容的URL。因为有时候会碰到301跳转等情况，最终访问的URL并非输入的URL
cookies:文本形式，对于某些要求登录的网站，登录之后，直接从浏览器中把Cookie复制进去即可
postXML:指待Post的XML数据，文本类型

例:
ga := gather.NewGather("chrome", false)
cookies := `SINAGLOBAL=8868584542946.604.1509350660873;??????????; YF-Page-G0=b9385a03a044baf8db46b84f3ff125a0`
postXML := `<?xml version="1.0" encoding="utf-8"?><login><user>ydg</user><password>abcdef</password></login>`
html, redirectURL, err := ga.PostXMLUtil(`https://weibo.com/xxxxx`, "", cookies, postXML)
*/
func (g *GatherStruct) PostXMLUtil(URL, refererURL, cookies, postXML string) (html, redirectURL string, err error) {
	g.locker.Lock()
	defer g.locker.Unlock()

	// 规范XML的Content-Type，补充utf-8
	if _, exist := g.safeHeaders.Load("Content-Type"); !exist {
		g.safeHeaders.Store("Content-Type", "application/xml; charset=utf-8")
	}

	req, err := g.newHttpRequest("POST", URL, refererURL, cookies, strings.NewReader(postXML))
	if err != nil {
		return "", "", err
	}
	return g.request(req)
}

/*
以json的方式post数据,自动继承先前的cookies
URL:指待抓取的URL
refererURL:上一次访问的URL。某些防抓取比较严格的网站会对上次访问的页面URL进行验证
redirectURL:最终实际访问到内容的URL。因为有时候会碰到301跳转等情况，最终访问的URL并非输入的URL
postJson:指待Post的json数据，文本类型

例:
ga := gather.NewGather("chrome", false)
postJson := `{"user":"ydg","password":"abcdef"}`
html, redirectURL, err := ga.PostJson(`https://weibo.com/xxxxx`, "", postJson)
*/
func (g *GatherStruct) PostJson(URL, refererURL, postJson string) (html, redirectURL string, err error) {
	return g.PostJsonUtil(URL, refererURL, "", postJson)
}

/*
以json的方式post数据,手动增加cookies
URL:指待抓取的URL
refererURL:上一次访问的URL。某些防抓取比较严格的网站会对上次访问的页面URL进行验证
redirectURL:最终实际访问到内容的URL。因为有时候会碰到301跳转等情况，最终访问的URL并非输入的URL
cookies:文本形式，对于某些要求登录的网站，登录之后，直接从浏览器中把Cookie复制进去即可
postJson:指待Post的json数据，文本类型

例:
ga := gather.NewGather("chrome", false)
cookies := `SINAGLOBAL=8868584542946.604.1509350660873;??????????; YF-Page-G0=b9385a03a044baf8db46b84f3ff125a0`
postJson := `{"user":"ydg","password":"abcdef"}`
html, redirectURL, err := ga.PostJsonUtil(`https://weibo.com/xxxxx`, "", cookies, postJson)
*/
func (g *GatherStruct) PostJsonUtil(URL, refererURL, cookies, postJson string) (html, redirectURL string, err error) {
	g.locker.Lock()
	defer g.locker.Unlock()

	// 规范JSON的Content-Type，补充utf-8
	if _, exist := g.safeHeaders.Load("Content-Type"); !exist {
		g.safeHeaders.Store("Content-Type", "application/json; charset=utf-8")
	}

	req, err := g.newHttpRequest("POST", URL, refererURL, cookies, strings.NewReader(postJson))
	if err != nil {
		return "", "", err
	}
	return g.request(req)
}

// MultipartPostFile multipart/form-data 上传文件的结构体（修正驼峰命名）
type MultipartPostFile struct {
	FileName    string // 文件名
	ContentType string // 文件MIME类型（如image/png、application/pdf）
	Content     []byte // 文件二进制内容
}

/*
PostMultipartFormData multipart/form-data方式POST数据,自动继承先前的cookies
boundary: multipart分割边界，为空则使用标准库生成的安全边界
postValueMap: 普通文本参数（name->value）
postFileMap: 上传文件参数（name->MultipartPostFile）
*/
func (g *GatherStruct) PostMultipartFormData(URL, refererURL, boundary string, postValueMap map[string]string, postFileMap map[string]MultipartPostFile) (html, redirectURL string, err error) {
	// 修复：原代码错误传参，现在正确传递空cookies（表示继承原有cookies）
	return g.PostMultipartFormDataUtil(URL, refererURL, "", boundary, postValueMap, postFileMap)
}

/*
PostMultipartFormDataUtil multipart/form-data方式POST数据,手动增加cookies
URL: 待抓取的URL
refererURL: 上一次访问的URL
cookies: 手动传入的cookies字符串
boundary: multipart分割边界，为空则使用标准库生成的安全边界
postValueMap: 普通文本参数（name->value）
postFileMap: 上传文件参数（name->MultipartPostFile）
核心修复：
1. 移除对textproto.Quote的依赖，改用自定义quote函数（兼容所有Go版本）；
2. 手动构建Part Header，确保文件Content-Type生效；
3. 避免双引号重复，符合HTTP规范；
4. 错误包装，便于问题定位。
*/
func (g *GatherStruct) PostMultipartFormDataUtil(URL, refererURL, cookies, boundary string, postValueMap map[string]string, postFileMap map[string]MultipartPostFile) (html, redirectURL string, err error) {
	g.locker.Lock()
	defer g.locker.Unlock()

	// 1. 初始化multipart writer
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	defer writer.Close()

	// 2. 设置自定义/默认boundary
	if boundary == "" {
		boundary = writer.Boundary() // 使用标准库生成的安全边界
	} else {
		if err := writer.SetBoundary(boundary); err != nil {
			return "", "", fmt.Errorf("设置multipart边界失败：%w", err)
		}
	}

	// 3. 添加普通文本参数
	for name, value := range postValueMap {
		if err := writer.WriteField(name, value); err != nil {
			return "", "", fmt.Errorf("添加文本参数[%s]失败：%w", name, err)
		}
	}

	// 4. 添加文件参数（终极修复：自定义quote函数，无版本依赖）
	for name, file := range postFileMap {
		// 4.1 构建Part的MIME Header（替代CreateFormFile的自动生成）
		header := make(textproto.MIMEHeader)
		// 设置Content-Disposition：使用自定义quote函数转义，无版本依赖，无重复引号
		header.Set("Content-Disposition",
			fmt.Sprintf(`form-data; name=%s; filename=%s`,
				quote(name),          // 转义参数名（兼容特殊字符）
				quote(file.FileName), // 转义文件名（兼容中文/空格/双引号）
			),
		)
		// 设置文件Content-Type（自定义值/默认值）
		if file.ContentType == "" {
			header.Set("Content-Type", "application/octet-stream") // 默认二进制类型
		} else {
			header.Set("Content-Type", file.ContentType)
		}

		// 4.2 创建自定义Header的Part（替代CreateFormFile，确保Content-Type生效）
		part, err := writer.CreatePart(header)
		if err != nil {
			return "", "", fmt.Errorf("创建文件Part[%s]失败：%w", name, err)
		}

		// 4.3 写入文件二进制内容
		if _, err := part.Write(file.Content); err != nil {
			return "", "", fmt.Errorf("写入文件[%s]内容失败：%w", file.FileName, err)
		}
	}

	// 5. 完成multipart数据构建
	if err := writer.Close(); err != nil {
		return "", "", fmt.Errorf("关闭multipart writer失败：%w", err)
	}

	// 6. 设置请求的Content-Type（包含boundary）
	g.safeHeaders.Store("Content-Type", writer.FormDataContentType())

	// 7. 构建HTTP请求
	req, err := g.newHttpRequest("POST", URL, refererURL, cookies, &body)
	if err != nil {
		return "", "", fmt.Errorf("构建POST请求失败：%w", err)
	}

	// 8. 执行请求并返回结果
	html, redirectURL, err = g.request(req)
	if err != nil {
		return "", "", fmt.Errorf("执行multipart POST请求失败：%w", err)
	}
	return html, redirectURL, nil
}
