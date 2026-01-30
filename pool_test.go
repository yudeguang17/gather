// gather_pool_test.go
package gather

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic" // 新增：导入原子操作包
	"testing"
	"time"
)

// TestNewGatherUtilPool 测试池初始化逻辑（无网络依赖）
func TestNewGatherUtilPool(t *testing.T) {
	testCases := []struct {
		name          string
		inputNum      int
		customConfig  PoolConfig
		expectedSize  int
		expectedRatio float64
	}{
		{
			name:          "默认配置-池大小5",
			inputNum:      5,
			customConfig:  defaultPoolConfig,
			expectedSize:  5,
			expectedRatio: 0.2,
		},
		{
			name:          "池大小超过MaxPoolSize-自动截断为100",
			inputNum:      200,
			customConfig:  defaultPoolConfig,
			expectedSize:  100,
			expectedRatio: 0.2,
		},
		{
			name:          "池大小为0-自动修正为1",
			inputNum:      0,
			customConfig:  defaultPoolConfig,
			expectedSize:  1,
			expectedRatio: 0.2,
		},
		{
			name:          "自定义非法Ratio-自动修正为0.2",
			inputNum:      3,
			customConfig:  PoolConfig{MaxIdleConnsPerHostRatio: 2.0},
			expectedSize:  3,
			expectedRatio: 0.2,
		},
		{
			name:     "自定义合法配置-生效",
			inputNum: 4,
			customConfig: PoolConfig{
				MaxIdleConnsPerHostRatio: 0.5,
				TimeoutSecond:            10,
				RetryIntervalMs:          200,
			},
			expectedSize:  4,
			expectedRatio: 0.5,
		},
	}

	emptyHeaders := make(map[string]string)
	emptyHeaders["User-Agent"] = "test-pool/1.0"

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pool := NewGatherUtilPoolWithConfig(
				emptyHeaders,
				"",
				5,
				false,
				tc.inputNum,
				tc.customConfig,
			)

			if len(pool.pool) != tc.expectedSize {
				t.Errorf("池大小不符合预期：期望%d，实际%d", tc.expectedSize, len(pool.pool))
			}

			if pool.config.MaxIdleConnsPerHostRatio != tc.expectedRatio {
				t.Errorf("MaxIdleConnsPerHostRatio不符合预期：期望%f，实际%f", tc.expectedRatio, pool.config.MaxIdleConnsPerHostRatio)
			}

			for i, ga := range pool.pool {
				if ga == nil || ga.Client == nil {
					t.Errorf("池内第%d个GatherStruct实例初始化失败", i)
				}
			}

			idleCount := 0
			pool.unUsed.Range(func(k, v interface{}) bool {
				if v.(bool) {
					idleCount++
				}
				return true
			})
			if idleCount != tc.expectedSize {
				t.Errorf("空闲实例数不符合预期：期望%d，实际%d", tc.expectedSize, idleCount)
			}
		})
	}
}

// TestPool_Get 测试Pool的基础Get方法（最终修复版）
func TestPool_Get(t *testing.T) {
	testGetURL := testBaseURL + "/get"

	headers := make(map[string]string)
	headers["User-Agent"] = "test-pool-get/1.0"
	pool := NewGatherUtilPool(headers, "", 10, false, 2)

	html, redirectURL, err := pool.Get(testGetURL, "")
	if err != nil {
		t.Fatalf("Pool.Get请求失败：%v", err)
	}

	var respData map[string]interface{}
	if err := json.Unmarshal([]byte(html), &respData); err != nil {
		t.Fatalf("解析返回JSON失败：%v，返回内容：%s", err, html)
	}

	// 验证URL
	respURL, ok := respData["url"].(string)
	if !ok {
		t.Fatalf("返回的url字段格式错误，期望string，实际%T", respData["url"])
	}
	if respURL != "/get" && !strings.HasSuffix(respURL, "/get") {
		t.Errorf("返回URL不符合预期：期望包含/get，实际%v", respURL)
	}

	// 验证Method
	respMethod, ok := respData["method"].(string)
	if !ok {
		t.Fatalf("返回的method字段格式错误，期望string，实际%T", respData["method"])
	}
	if respMethod != "GET" {
		t.Errorf("返回Method不符合预期：期望GET，实际%v", respMethod)
	}

	// 验证User-Agent
	respHeaders, ok := respData["headers"].(map[string]interface{})
	if !ok || respHeaders == nil {
		t.Fatalf("返回的headers字段格式错误，无法转为map：%v", respData["headers"])
	}
	userAgent := ""
	for k, v := range respHeaders {
		if strings.EqualFold(k, "User-Agent") {
			userAgent = fmt.Sprintf("%v", v)
			break
		}
	}
	if userAgent != "test-pool-get/1.0" {
		t.Errorf("User-Agent未生效：期望test-pool-get/1.0，实际%v", userAgent)
	}

	// 验证跳转URL
	if redirectURL != testGetURL {
		t.Errorf("跳转URL不符合预期：期望%s，实际%s", testGetURL, redirectURL)
	}

	// 验证实例复用
	idleCount := 0
	pool.unUsed.Range(func(k, v interface{}) bool {
		if v.(bool) {
			idleCount++
		}
		return true
	})
	if idleCount != 2 {
		t.Errorf("请求后空闲实例数不符合预期：期望2，实际%d", idleCount)
	}
}

// TestPool_GetUtil 测试Pool的GetUtil方法（最终修复版）
func TestPool_GetUtil(t *testing.T) {
	testCookieURL := testBaseURL + "/cookies"

	headers := make(map[string]string)
	pool := NewGatherUtilPool(headers, "", 10, false, 1)

	customCookie := "test_key=test_value; pool_id=123"
	html, _, err := pool.GetUtil(testCookieURL, "", customCookie)
	if err != nil {
		t.Fatalf("Pool.GetUtil请求失败：%v", err)
	}

	var respData map[string]interface{}
	if err := json.Unmarshal([]byte(html), &respData); err != nil {
		t.Fatalf("解析返回JSON失败：%v，返回内容：%s", err, html)
	}

	// 验证Cookie
	cookiesData, ok := respData["cookies"].(map[string]interface{})
	if !ok || cookiesData == nil {
		t.Fatalf("返回的cookies字段格式错误，无法转为map：%v", respData["cookies"])
	}
	if cookiesData["test_key"] != "test_value" {
		t.Errorf("Cookie test_key未生效：期望test_value，实际%v", cookiesData["test_key"])
	}
	if cookiesData["pool_id"] != "123" {
		t.Errorf("Cookie pool_id未生效：期望123，实际%v", cookiesData["pool_id"])
	}
}

// TestPool_Post 测试Pool的Post方法（最终修复版）
func TestPool_Post(t *testing.T) {
	testPostURL := testBaseURL + "/post"

	headers := make(map[string]string)
	pool := NewGatherUtilPool(headers, "", 10, false, 2)

	postMap := map[string]string{
		"name":  "test_pool",
		"value": "123456",
	}
	html, _, err := pool.Post(testPostURL, "", postMap)
	if err != nil {
		t.Fatalf("Pool.Post请求失败：%v", err)
	}

	var respData map[string]interface{}
	if err := json.Unmarshal([]byte(html), &respData); err != nil {
		t.Fatalf("解析返回JSON失败：%v，返回内容：%s", err, html)
	}

	// 验证Method
	respMethod, ok := respData["method"].(string)
	if !ok {
		t.Fatalf("返回的method字段格式错误，期望string，实际%T", respData["method"])
	}
	if respMethod != "POST" {
		t.Errorf("返回Method不符合预期：期望POST，实际%v", respMethod)
	}

	// 验证Post参数（修复后：值为字符串，非数组）
	postForm, ok := respData["form"].(map[string]interface{})
	if !ok || postForm == nil {
		t.Fatalf("返回的form字段格式错误，无法转为map：%v", respData["form"])
	}
	if postForm["name"] != "test_pool" {
		t.Errorf("Post参数name未生效：期望test_pool，实际%v", postForm["name"])
	}
	if postForm["value"] != "123456" {
		t.Errorf("Post参数value未生效：期望123456，实际%v", postForm["value"])
	}
}

// TestPool_Concurrent 测试Pool的并发安全性（优化版：50协程，修复原子类型）
func TestPool_Concurrent(t *testing.T) {
	testGetURL := testBaseURL + "/get"

	headers := make(map[string]string)
	headers["User-Agent"] = "concurrent-test/1.0"
	pool := NewGatherUtilPool(headers, "", 10, false, 5) // 池大小5，并发50协程

	// 提升并发量到50（典型高并发场景：并发量=10*池大小）
	concurrency := 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	errChan := make(chan error, concurrency)
	var successCount atomic.Int32 // 修复：正确的原子类型（需导入sync/atomic）

	// 记录开始时间，统计响应耗时
	startTime := time.Now()

	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()
			t.Logf("协程%d开始请求", idx)
			html, _, err := pool.Get(testGetURL, "")
			if err != nil {
				errChan <- fmt.Errorf("协程%d请求失败：%v", idx, err)
				return
			}
			successCount.Add(1) // 原子增加成功数

			if len(html) == 0 {
				errChan <- fmt.Errorf("协程%d返回内容为空", idx)
				return
			}
			var respData map[string]interface{}
			if err := json.Unmarshal([]byte(html), &respData); err != nil {
				errChan <- fmt.Errorf("协程%d解析JSON失败：%v", idx, err)
				return
			}
			method, ok := respData["method"].(string)
			if !ok || method != "GET" {
				errChan <- fmt.Errorf("协程%d返回method错误：%v", idx, method)
			}
			t.Logf("协程%d请求完成", idx)
		}(i)
	}

	wg.Wait()
	close(errChan)
	elapsed := time.Since(startTime)

	// 打印并发统计信息
	t.Logf("=== 并发测试统计 ===")
	t.Logf("总协程数：%d", concurrency)
	t.Logf("成功数：%d", successCount.Load()) // 读取原子变量值
	t.Logf("失败数：%d", len(errChan))
	t.Logf("总耗时：%v", elapsed)
	t.Logf("平均耗时：%v", elapsed/time.Duration(concurrency))

	if len(errChan) > 0 {
		t.Errorf("并发请求失败，错误数：%d", len(errChan))
		for err := range errChan {
			t.Error(err)
		}
	} else {
		t.Logf("✅ %d协程并发请求全部成功，池大小5，验证实例复用正常", concurrency)
	}
}

// TestPool_Concurrent_HighPressure 测试Pool的高压并发（200协程，极限场景，修复原子类型）
func TestPool_Concurrent_HighPressure(t *testing.T) {
	// 跳过短测试模式（如果启用）
	if testing.Short() {
		t.Skip("短测试模式下跳过高压并发测试")
	}

	testGetURL := testBaseURL + "/get"

	headers := make(map[string]string)
	headers["User-Agent"] = "high-pressure-test/1.0"
	pool := NewGatherUtilPool(headers, "", 10, false, 10) // 池大小10，200协程

	concurrency := 200 // 高压并发：200协程
	var wg sync.WaitGroup
	wg.Add(concurrency)

	errChan := make(chan error, concurrency)
	var successCount atomic.Int32 // 修复：正确的原子类型
	startTime := time.Now()

	// 高压并发请求
	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()
			// 每个请求加微小延迟，避免瞬间打满CPU
			time.Sleep(time.Millisecond * 5)

			html, _, err := pool.Get(testGetURL, "")
			if err != nil {
				errChan <- fmt.Errorf("高压协程%d请求失败：%v", idx, err)
				return
			}
			successCount.Add(1) // 原子增加成功数

			// 仅验证核心：返回内容非空
			if len(html) == 0 {
				errChan <- fmt.Errorf("高压协程%d返回内容为空", idx)
			}
		}(i)
	}

	wg.Wait()
	close(errChan)
	elapsed := time.Since(startTime)

	// 高压测试统计
	t.Logf("=== 高压并发测试统计 ===")
	t.Logf("总协程数：%d", concurrency)
	t.Logf("成功数：%d", successCount.Load()) // 读取原子变量值
	t.Logf("失败数：%d", len(errChan))
	t.Logf("总耗时：%v", elapsed)
	t.Logf("QPS：%.2f", float64(successCount.Load())/elapsed.Seconds())

	// 高压场景允许少量错误（根据实际需求调整阈值）
	errorRate := float64(len(errChan)) / float64(concurrency)
	if errorRate > 0.05 { // 错误率超过5%则失败
		t.Errorf("高压并发测试错误率过高：%.2f%%（阈值5%%）", errorRate*100)
		for err := range errChan {
			t.Error(err)
		}
	} else {
		t.Logf("✅ 200协程高压并发测试通过，错误率：%.2f%%", errorRate*100)
	}
}

// TestPool_Timeout 测试Pool的超时控制（终极修复版）
func TestPool_Timeout(t *testing.T) {
	testGetURL := testBaseURL + "/get"

	// 创建极小池（大小1），自定义池的获取超时（2秒）
	customConfig := PoolConfig{
		TimeoutSecond:   2, // 池获取实例的超时时间
		RetryIntervalMs: 100,
		IsUseSemaphore:  true,
	}
	headers := make(map[string]string)
	pool := NewGatherUtilPoolWithConfig(headers, "", 10, false, 1, customConfig) // Client超时设为10秒

	// ========== 核心修复：手动占用唯一实例（标记为已使用，不归还） ==========
	var targetKey interface{}
	// 遍历unUsed map，找到唯一的实例key
	pool.unUsed.Range(func(k, v interface{}) bool {
		if v.(bool) { // 找到空闲实例
			targetKey = k
			pool.unUsed.Store(k, false) // 标记为已使用
			return false
		}
		return true
	})
	if targetKey == nil {
		t.Fatal("池内无空闲实例，测试无法进行")
	}

	// 等待池状态更新
	time.Sleep(500 * time.Millisecond)

	// 再次调用Get，应触发「池无空闲实例+超时」错误
	html, redirectURL, err := pool.Get(testGetURL, "")

	// 验证错误类型
	if err == nil {
		t.Error("池满时Get未返回超时错误，不符合预期")
	} else if err != errNoFreeClinetFind {
		t.Errorf("超时错误类型不符合预期：期望%v，实际%v", errNoFreeClinetFind, err)
	}

	// 验证返回值为空
	if html != "" || redirectURL != "" {
		t.Error("超时场景返回非空内容，不符合预期")
	}

	// 还原池状态（可选，不影响测试结果）
	pool.unUsed.Store(targetKey, true)
	t.Logf("✅ 池满超时测试通过，返回预期错误：%v", errNoFreeClinetFind)
}

// TestPool_PostUtil 测试Pool的PostUtil方法（最终修复版）
func TestPool_PostUtil(t *testing.T) {
	testPostURL := testBaseURL + "/post"

	headers := make(map[string]string)
	pool := NewGatherUtilPool(headers, "", 10, false, 1)

	// 自定义Cookie + Post参数
	customCookie := "post_test=123; user=pool"
	postMap := map[string]string{
		"post_key": "post_value",
	}

	html, _, err := pool.PostUtil(testPostURL, "", customCookie, postMap)
	if err != nil {
		t.Fatalf("Pool.PostUtil请求失败：%v", err)
	}

	var respData map[string]interface{}
	if err := json.Unmarshal([]byte(html), &respData); err != nil {
		t.Fatalf("解析返回JSON失败：%v，返回内容：%s", err, html)
	}

	// 验证Post参数
	postForm, ok := respData["form"].(map[string]interface{})
	if !ok || postForm == nil {
		t.Fatalf("返回的form字段格式错误，无法转为map：%v", respData["form"])
	}
	if postForm["post_key"] != "post_value" {
		t.Errorf("Post参数未生效，返回Form：%v", postForm)
	}

	// 验证Cookie（适配修复后的路由：cookies是map[string]string）
	cookies, ok := respData["cookies"].(map[string]interface{})
	if !ok || cookies == nil {
		t.Fatalf("返回的cookies字段格式错误，无法转为map：%v", respData["cookies"])
	}
	if cookies["post_test"] != "123" || cookies["user"] != "pool" {
		t.Errorf("自定义Cookie未生效，返回Cookie：%v", cookies)
	}
}
