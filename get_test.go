// gather_test.go
package gather

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var (
	testServer  *httptest.Server // 全局测试Server
	testBaseURL string           // 测试Server基础URL
)

// TestMain 测试入口：启动本地Server（整合GET/POST/POOL所有测试接口），执行测试后关闭
func TestMain(m *testing.M) {
	mux := http.NewServeMux()

	// -------------------------- GET 测试接口 --------------------------
	// /get：基础GET测试，返回请求信息
	mux.HandleFunc("/get", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		headers := make(map[string]interface{})
		for k, v := range r.Header {
			if len(v) == 1 {
				headers[k] = v[0]
			} else {
				headers[k] = v
			}
		}
		resp := map[string]interface{}{
			"status":  "success",
			"content": "local test content",
			"url":     r.URL.String(),
			"method":  r.Method,
			"headers": headers,
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	// /cookies：GET测试Cookie传递
	mux.HandleFunc("/cookies", func(w http.ResponseWriter, r *http.Request) {
		cookies := make(map[string]string)
		for _, c := range r.Cookies() {
			cookies[c.Name] = c.Value
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"cookies": cookies,
		})
	})

	// /timeout：GET测试超时场景
	mux.HandleFunc("/timeout", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.Write([]byte("timeout response"))
	})

	// /404：GET测试无效URL场景
	mux.HandleFunc("/404", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("404 Not Found"))
	})

	// -------------------------- POST 测试接口 --------------------------
	// /post：普通POST（表单/JSON/XML/二进制）测试
	mux.HandleFunc("/post", func(w http.ResponseWriter, r *http.Request) {
		var postData map[string]interface{}
		contentType := r.Header.Get("Content-Type")

		switch {
		// 表单类型
		case strings.Contains(contentType, "application/x-www-form-urlencoded"):
			_ = r.ParseForm()
			postData = make(map[string]interface{})
			for k, v := range r.PostForm {
				postData[k] = v[0]
			}
		// JSON类型
		case strings.Contains(contentType, "application/json"):
			_ = json.NewDecoder(r.Body).Decode(&postData)
		// XML类型
		case strings.Contains(contentType, "application/xml"):
			postData = map[string]interface{}{
				"xml_content": strings.TrimSpace(r.PostFormValue("xml")),
			}
		// 二进制类型
		case strings.Contains(contentType, "application/octet-stream"):
			body, _ := io.ReadAll(r.Body)
			postData = map[string]interface{}{
				"binary_content": string(body),
			}
		default:
			postData = map[string]interface{}{
				"error": "unsupported content type",
			}
		}

		// 返回解析结果
		cookies := make(map[string]string)
		for _, c := range r.Cookies() {
			cookies[c.Name] = c.Value
		}
		respData := map[string]interface{}{
			"method":       r.Method,
			"form":         postData,
			"cookies":      cookies,
			"content_type": contentType,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(respData)
	})

	// /upload：multipart/form-data文件上传测试
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		// 解析multipart表单（最大10MB）
		err := r.ParseMultipartForm(10 << 20)
		if err != nil {
			http.Error(w, fmt.Sprintf("解析multipart失败：%v", err), http.StatusBadRequest)
			return
		}

		// 读取文本参数
		textParams := make(map[string]string)
		for k, v := range r.MultipartForm.Value {
			textParams[k] = v[0]
		}

		// 读取文件参数
		fileParams := make(map[string]interface{})
		for k, v := range r.MultipartForm.File {
			if len(v) > 0 {
				file, err := v[0].Open()
				if err != nil {
					fileParams[k] = fmt.Sprintf("打开文件失败：%v", err)
					continue
				}
				defer file.Close()

				content, err := io.ReadAll(file)
				if err != nil {
					fileParams[k] = fmt.Sprintf("读取文件失败：%v", err)
					continue
				}

				fileParams[k] = map[string]interface{}{
					"filename":     v[0].Filename,
					"size":         len(content),
					"content":      string(content),
					"content_type": v[0].Header.Get("Content-Type"),
				}
			}
		}

		// 返回结果
		respData := map[string]interface{}{
			"method":       r.Method,
			"text_params":  textParams,
			"file_params":  fileParams,
			"content_type": r.Header.Get("Content-Type"),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(respData)
	})

	// -------------------------- POOL 测试接口 --------------------------
	// /pool：POOL高并发测试专用接口（轻量响应，避免Server成为瓶颈）
	mux.HandleFunc("/pool", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 模拟轻量业务逻辑（1ms延迟）
		time.Sleep(1 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "success",
			"pool_id": r.URL.Query().Get("pool_id"), // 接收POOL测试的标识参数
			"time":    time.Now().UnixMilli(),
		})
	})

	// 启动测试Server
	testServer = httptest.NewServer(mux)
	testBaseURL = testServer.URL

	// 执行所有测试（GET/POST/POOL）
	exitCode := m.Run()

	// 释放资源
	testServer.Close()
	os.Exit(exitCode)
}

// TestGather_Get 测试原生Gather基础GET功能（修复JSON解析问题）
func TestGather_Get(t *testing.T) {
	ga := NewGather("chrome", false)
	if ga == nil {
		t.Fatal("NewGather创建实例失败")
	}

	// 临时设置超时，测试后还原
	originalTimeout := ga.Client.Timeout
	ga.Client.Timeout = 10 * time.Second
	defer func() { ga.Client.Timeout = originalTimeout }()

	localGetURL := testBaseURL + "/get"
	local404URL := testBaseURL + "/404"
	localTimeoutURL := testBaseURL + "/timeout"

	testCases := []struct {
		name        string
		url         string
		referer     string
		wantErr     bool
		checkFunc   func(t *testing.T, html, redirectURL string)
		timeout     int
		skipNetwork bool
	}{
		{
			name:    "正常请求本地GET接口",
			url:     localGetURL,
			referer: "",
			wantErr: false,
			checkFunc: func(t *testing.T, html, redirectURL string) {
				var respData map[string]interface{}
				if err := json.Unmarshal([]byte(html), &respData); err != nil {
					t.Fatalf("解析本地返回JSON失败：%v，内容：%s", err, html)
				}
				// 验证核心字段
				status, ok := respData["status"].(string)
				if !ok || status != "success" {
					t.Errorf("status字段异常，期望success，实际%v", status)
				}
				content, ok := respData["content"].(string)
				if !ok || content != "local test content" {
					t.Errorf("content字段异常，期望local test content，实际%v", content)
				}
				// 验证跳转地址
				if redirectURL != localGetURL {
					t.Errorf("跳转URL异常，期望%s，实际%s", localGetURL, redirectURL)
				}
			},
			timeout:     10,
			skipNetwork: false,
		},
		{
			name:        "无效URL测试（本地404）",
			url:         local404URL,
			referer:     "",
			wantErr:     true,
			checkFunc:   func(t *testing.T, html, redirectURL string) {},
			timeout:     5,
			skipNetwork: false,
		},
		{
			name:        "超时请求测试（本地timeout）",
			url:         localTimeoutURL,
			referer:     "",
			wantErr:     true,
			checkFunc:   func(t *testing.T, html, redirectURL string) {},
			timeout:     1, // 毫秒级超时，强制触发
			skipNetwork: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipNetwork {
				t.Skip("跳过网络请求测试")
			}

			// 临时修改客户端超时
			tempClient := *ga.Client
			if tc.name == "超时请求测试（本地timeout）" {
				tempClient.Timeout = time.Duration(tc.timeout) * time.Millisecond
			} else {
				tempClient.Timeout = time.Duration(tc.timeout) * time.Second
			}
			originalClient := ga.Client
			ga.Client = &tempClient
			defer func() { ga.Client = originalClient }()

			// 执行请求
			html, redirectURL, err := ga.Get(tc.url, tc.referer)
			if (err != nil) != tc.wantErr {
				t.Errorf("错误状态不符：期望错误%v，实际%v", tc.wantErr, err)
				return
			}
			if !tc.wantErr {
				tc.checkFunc(t, html, redirectURL)
			}
		})
	}
}

// TestGather_GetUtil 测试原生Gather带Cookie的GetUtil
func TestGather_GetUtil(t *testing.T) {
	ga := NewGather("chrome", false)
	if ga == nil {
		t.Fatal("NewGather创建实例失败")
	}

	localCookieURL := testBaseURL + "/cookies"
	t.Run("GetUtil带自定义Cookie", func(t *testing.T) {
		customCookie := "test_key=test_value; user_id=123456"
		html, redirectURL, err := ga.GetUtil(localCookieURL, "", customCookie)
		if err != nil {
			t.Fatalf("GetUtil请求失败：%v", err)
		}
		// 解析验证Cookie
		var respData map[string]interface{}
		if err := json.Unmarshal([]byte(html), &respData); err != nil {
			t.Fatalf("解析Cookie返回JSON失败：%v", err)
		}
		cookies, ok := respData["cookies"].(map[string]interface{})
		if !ok || cookies == nil {
			t.Fatal("返回Cookie字段格式错误")
		}
		if cookies["test_key"] != "test_value" || cookies["user_id"] != "123456" {
			t.Errorf("自定义Cookie未生效：%v", cookies)
		}
		if redirectURL != localCookieURL {
			t.Errorf("跳转URL异常：%s", redirectURL)
		}
	})
}

// TestGather_ConcurrentGET 【原生GET基础并发测试】验证协程安全（10协程）
func TestGather_ConcurrentGET(t *testing.T) {
	ga := NewGather("chrome", false)
	if ga == nil {
		t.Fatal("NewGather创建实例失败")
	}

	localGetURL := testBaseURL + "/get"
	concurrency := 10 // 基础并发数
	var wg sync.WaitGroup
	wg.Add(concurrency)

	errChan := make(chan error, concurrency)

	// 多协程同时调用原生GET
	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()
			log.Printf("原生GET协程%d开始请求", idx)
			html, _, err := ga.Get(localGetURL, "")
			if err != nil {
				errChan <- fmt.Errorf("协程%d失败：%v", idx, err)
				return
			}
			// 简单验证返回有效
			if len(html) == 0 || !strings.Contains(html, "success") {
				errChan <- fmt.Errorf("协程%d返回内容异常", idx)
			}
			log.Printf("原生GET协程%d请求完成", idx)
		}(i)
	}

	wg.Wait()
	close(errChan)

	// 验证无并发错误
	if len(errChan) > 0 {
		t.Errorf("原生GET并发测试失败，错误数：%d", len(errChan))
		for err := range errChan {
			t.Error(err)
		}
	} else {
		t.Logf("✅ 原生GET %d协程基础并发测试通过，协程安全", concurrency)
	}
}

// TestGather_Concurrent_High 【原生GET高并发压力测试】验证高并发下稳定性（50协程）
func TestGather_Concurrent_High(t *testing.T) {
	// 短测试模式下可跳过
	if testing.Short() {
		t.Skip("短测试模式跳过原生GET高并发测试")
	}

	ga := NewGather("chrome", false)
	if ga == nil {
		t.Fatal("NewGather创建实例失败")
	}

	localGetURL := testBaseURL + "/get"
	concurrency := 50 // 高并发：50协程同时调用原生GET
	var wg sync.WaitGroup
	wg.Add(concurrency)

	errChan := make(chan error, concurrency)
	var successCnt atomic.Int32 // 原子统计成功数
	startTime := time.Now()

	// 高并发请求
	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()
			// 调用原生GET，无任何池化，直接使用http.Client
			html, _, err := ga.Get(localGetURL, "")
			if err != nil {
				errChan <- fmt.Errorf("高并发协程%d失败：%v", idx, err)
				return
			}
			successCnt.Add(1)
			// 轻量验证
			if len(html) == 0 {
				errChan <- fmt.Errorf("高并发协程%d返回空内容", idx)
			}
		}(i)
	}

	wg.Wait()
	close(errChan)
	elapsed := time.Since(startTime)

	// 输出高并发统计
	t.Logf("=== 原生GET高并发测试统计 ===")
	t.Logf("总协程数：%d", concurrency)
	t.Logf("成功请求数：%d", successCnt.Load())
	t.Logf("失败请求数：%d", len(errChan))
	t.Logf("总耗时：%v", elapsed)
	t.Logf("QPS：%.2f", float64(successCnt.Load())/elapsed.Seconds())

	// 验证：高并发下无大量错误，证明协程安全
	errorRate := float64(len(errChan)) / float64(concurrency)
	if errorRate > 0.03 { // 允许3%以内错误（网络/本地调度波动）
		t.Errorf("原生GET高并发错误率过高：%.2f%%", errorRate*100)
		for err := range errChan {
			t.Error(err)
		}
	} else {
		t.Logf("✅ 原生GET 50协程高并发测试通过，协程安全，稳定性符合预期")
		t.Logf("注意：原生GET仅保证协程安全，无池化优化，高并发下资源开销大于Pool版")
	}
}

// TestGather_EdgeCases 测试原生Gather边缘场景
func TestGather_EdgeCases(t *testing.T) {
	ga := NewGather("chrome", false)
	if ga == nil {
		t.Fatal("NewGather创建实例失败")
	}

	// 测试空URL
	t.Run("空URL请求", func(t *testing.T) {
		_, _, err := ga.Get("", "")
		if err == nil {
			t.Error("空URL应返回错误，实际未返回")
		}
	})

	// 测试代理场景（无代理则跳过）
	t.Run("带代理GET请求", func(t *testing.T) {
		proxyURL := ""
		if proxyURL == "" {
			t.Skip("未配置测试代理，跳过该用例")
		}
		gaProxy := NewGatherProxy("chrome", proxyURL, false)
		html, redirectURL, err := gaProxy.Get(testBaseURL+"/get", "")
		if err != nil {
			t.Fatalf("代理请求失败：%v", err)
		}
		if !strings.Contains(html, "success") {
			t.Error("代理请求返回内容异常")
		}
		t.Logf("代理请求跳转URL：%s", redirectURL)
	})
}
