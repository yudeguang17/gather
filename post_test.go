// gather_post_test.go （可单独创建此文件，或追加到原有 gather_test.go）
package gather

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestGather_Post 测试普通表单POST功能
func TestGather_Post(t *testing.T) {
	ga := NewGather("chrome", false)
	if ga == nil {
		t.Fatal("NewGather创建实例失败")
	}

	localPostURL := testBaseURL + "/post"
	postMap := map[string]string{
		"user":     "ydg",
		"password": "123456",
	}

	// 执行普通POST
	html, redirectURL, err := ga.Post(localPostURL, "", postMap)
	if err != nil {
		t.Fatalf("普通POST请求失败：%v", err)
	}

	// 验证结果
	var respData map[string]interface{}
	if err := json.Unmarshal([]byte(html), &respData); err != nil {
		t.Fatalf("解析POST返回结果失败：%v", err)
	}

	// 验证参数是否正确接收
	formData, ok := respData["form"].(map[string]interface{})
	if !ok {
		t.Fatal("POST返回的form字段格式错误")
	}
	if formData["user"] != "ydg" || formData["password"] != "123456" {
		t.Errorf("POST参数接收错误，期望{user:ydg,password:123456}，实际%v", formData)
	}

	// 验证Content-Type
	contentType, ok := respData["content_type"].(string)
	if !ok || !strings.Contains(contentType, "application/x-www-form-urlencoded") {
		t.Errorf("Content-Type错误，期望application/x-www-form-urlencoded，实际%v", contentType)
	}

	// 验证跳转URL
	if redirectURL != localPostURL {
		t.Errorf("跳转URL错误，期望%s，实际%s", localPostURL, redirectURL)
	}
}

// TestGather_PostJson 测试JSON格式POST
func TestGather_PostJson(t *testing.T) {
	ga := NewGather("chrome", false)
	if ga == nil {
		t.Fatal("NewGather创建实例失败")
	}

	localPostURL := testBaseURL + "/post"
	postJson := `{"user":"ydg","password":"123456"}`

	// 执行JSON POST
	html, _, err := ga.PostJson(localPostURL, "", postJson)
	if err != nil {
		t.Fatalf("JSON POST请求失败：%v", err)
	}

	// 验证结果
	var respData map[string]interface{}
	if err := json.Unmarshal([]byte(html), &respData); err != nil {
		t.Fatalf("解析JSON POST返回结果失败：%v", err)
	}

	// 验证参数
	formData, ok := respData["form"].(map[string]interface{})
	if !ok {
		t.Fatal("JSON POST返回的form字段格式错误")
	}
	if formData["user"] != "ydg" || formData["password"] != "123456" {
		t.Errorf("JSON POST参数接收错误，期望{user:ydg,password:123456}，实际%v", formData)
	}

	// 验证Content-Type
	contentType, ok := respData["content_type"].(string)
	if !ok || !strings.Contains(contentType, "application/json") {
		t.Errorf("Content-Type错误，期望application/json，实际%v", contentType)
	}
}

// TestGather_PostXML 测试XML格式POST
func TestGather_PostXML(t *testing.T) {
	ga := NewGather("chrome", false)
	if ga == nil {
		t.Fatal("NewGather创建实例失败")
	}

	localPostURL := testBaseURL + "/post"
	postXML := `<?xml version="1.0" encoding="utf-8"?><login><user>ydg</user><password>123456</password></login>`

	// 执行XML POST
	html, _, err := ga.PostXML(localPostURL, "", postXML)
	if err != nil {
		t.Fatalf("XML POST请求失败：%v", err)
	}

	// 验证Content-Type
	var respData map[string]interface{}
	if err := json.Unmarshal([]byte(html), &respData); err != nil {
		t.Fatalf("解析XML POST返回结果失败：%v", err)
	}
	contentType, ok := respData["content_type"].(string)
	if !ok || !strings.Contains(contentType, "application/xml") {
		t.Errorf("Content-Type错误，期望application/xml，实际%v", contentType)
	}
}

// TestGather_PostBytes 测试二进制POST
func TestGather_PostBytes(t *testing.T) {
	ga := NewGather("chrome", false)
	if ga == nil {
		t.Fatal("NewGather创建实例失败")
	}

	localPostURL := testBaseURL + "/post"
	postBytes := []byte("binary test content")

	// 执行二进制POST
	html, _, err := ga.PostBytes(localPostURL, "", "", postBytes)
	if err != nil {
		t.Fatalf("二进制POST请求失败：%v", err)
	}

	// 验证结果
	var respData map[string]interface{}
	if err := json.Unmarshal([]byte(html), &respData); err != nil {
		t.Fatalf("解析二进制POST返回结果失败：%v", err)
	}

	// 验证二进制内容
	formData, ok := respData["form"].(map[string]interface{})
	if !ok {
		t.Fatal("二进制POST返回的form字段格式错误")
	}
	if formData["binary_content"] != string(postBytes) {
		t.Errorf("二进制内容接收错误，期望%v，实际%v", string(postBytes), formData["binary_content"])
	}
}

// TestGather_PostMultipartFormData 测试文件上传（multipart/form-data）
func TestGather_PostMultipartFormData(t *testing.T) {
	ga := NewGather("chrome", false)
	if ga == nil {
		t.Fatal("NewGather创建实例失败")
	}

	localUploadURL := testBaseURL + "/upload"
	// 构建文本参数
	textParams := map[string]string{
		"username": "ydg",
		"desc":     "test upload",
	}
	// 构建文件参数
	fileContent := []byte("test image content")
	fileParams := map[string]MultipartPostFile{
		"avatar": {
			FileName:    "test.png",
			ContentType: "image/png",
			Content:     fileContent,
		},
	}

	// 执行文件上传
	html, _, err := ga.PostMultipartFormData(localUploadURL, "", "", textParams, fileParams)
	if err != nil {
		t.Fatalf("文件上传失败：%v", err)
	}

	// 验证结果
	var respData map[string]interface{}
	if err := json.Unmarshal([]byte(html), &respData); err != nil {
		t.Fatalf("解析文件上传返回结果失败：%v", err)
	}

	// 验证文本参数
	textParamsResp, ok := respData["text_params"].(map[string]interface{})
	if !ok {
		t.Fatal("文件上传返回的text_params字段格式错误")
	}
	if textParamsResp["username"] != "ydg" || textParamsResp["desc"] != "test upload" {
		t.Errorf("文本参数接收错误，期望%v，实际%v", textParams, textParamsResp)
	}

	// 验证文件参数
	fileParamsResp, ok := respData["file_params"].(map[string]interface{})
	if !ok {
		t.Fatal("文件上传返回的file_params字段格式错误")
	}
	avatarData, ok := fileParamsResp["avatar"].(map[string]interface{})
	if !ok {
		t.Fatal("文件参数avatar格式错误")
	}
	if avatarData["filename"] != "test.png" || avatarData["content_type"] != "image/png" || avatarData["content"] != string(fileContent) {
		t.Errorf("文件参数接收错误，期望{filename:test.png, content_type:image/png, content:%v}，实际%v", string(fileContent), avatarData)
	}
}

// TestGather_ConcurrentPOST 【普通POST高并发测试】验证协程安全（50协程）
func TestGather_ConcurrentPOST(t *testing.T) {
	if testing.Short() {
		t.Skip("短测试模式跳过POST高并发测试")
	}

	ga := NewGather("chrome", false)
	if ga == nil {
		t.Fatal("NewGather创建实例失败")
	}

	localPostURL := testBaseURL + "/post"
	concurrency := 50 // 高并发数
	var wg sync.WaitGroup
	wg.Add(concurrency)

	errChan := make(chan error, concurrency)
	var successCnt atomic.Int32
	startTime := time.Now()

	// 多协程并发POST
	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()
			postMap := map[string]string{
				"user":  fmt.Sprintf("ydg_%d", idx),
				"token": fmt.Sprintf("token_%d", idx),
			}
			log.Printf("POST高并发协程%d开始请求", idx)
			html, _, err := ga.Post(localPostURL, "", postMap)
			if err != nil {
				errChan <- fmt.Errorf("协程%d失败：%v", idx, err)
				return
			}
			// 轻量验证返回有效
			if len(html) == 0 || !strings.Contains(html, "form") {
				errChan <- fmt.Errorf("协程%d返回内容异常", idx)
				return
			}
			successCnt.Add(1)
			log.Printf("POST高并发协程%d请求完成", idx)
		}(i)
	}

	wg.Wait()
	close(errChan)
	elapsed := time.Since(startTime)

	// 输出统计
	t.Logf("=== 普通POST高并发测试统计 ===")
	t.Logf("总协程数：%d", concurrency)
	t.Logf("成功请求数：%d", successCnt.Load())
	t.Logf("失败请求数：%d", len(errChan))
	t.Logf("总耗时：%v", elapsed)
	t.Logf("QPS：%.2f", float64(successCnt.Load())/elapsed.Seconds())

	// 验证：错误率不超过3%
	errorRate := float64(len(errChan)) / float64(concurrency)
	if errorRate > 0.03 {
		t.Errorf("普通POST高并发错误率过高：%.2f%%", errorRate*100)
		for err := range errChan {
			t.Error(err)
		}
	} else {
		t.Logf("✅ 普通POST 50协程高并发测试通过，协程安全，稳定性符合预期")
	}
}

// TestGather_ConcurrentMultipartPOST 【文件上传高并发测试】验证multipart POST协程安全（10协程）
func TestGather_ConcurrentMultipartPOST(t *testing.T) {
	if testing.Short() {
		t.Skip("短测试模式跳过Multipart POST高并发测试")
	}

	ga := NewGather("chrome", false)
	if ga == nil {
		t.Fatal("NewGather创建实例失败")
	}

	localUploadURL := testBaseURL + "/upload"
	concurrency := 10 // 文件上传并发数（不宜过高，避免本地资源耗尽）
	var wg sync.WaitGroup
	wg.Add(concurrency)

	errChan := make(chan error, concurrency)
	var successCnt atomic.Int32
	startTime := time.Now()

	// 多协程并发文件上传
	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()
			// 构建参数
			textParams := map[string]string{
				"username": fmt.Sprintf("ydg_%d", idx),
			}
			fileParams := map[string]MultipartPostFile{
				"file": {
					FileName:    fmt.Sprintf("test_%d.txt", idx),
					ContentType: "text/plain",
					Content:     []byte(fmt.Sprintf("test content %d", idx)),
				},
			}

			log.Printf("Multipart POST协程%d开始上传", idx)
			html, _, err := ga.PostMultipartFormData(localUploadURL, "", "", textParams, fileParams)
			if err != nil {
				errChan <- fmt.Errorf("协程%d上传失败：%v", idx, err)
				return
			}
			// 轻量验证
			if len(html) == 0 || !strings.Contains(html, "file_params") {
				errChan <- fmt.Errorf("协程%d返回内容异常", idx)
				return
			}
			successCnt.Add(1)
			log.Printf("Multipart POST协程%d上传完成", idx)
		}(i)
	}

	wg.Wait()
	close(errChan)
	elapsed := time.Since(startTime)

	// 输出统计
	t.Logf("=== Multipart POST高并发测试统计 ===")
	t.Logf("总协程数：%d", concurrency)
	t.Logf("成功上传数：%d", successCnt.Load())
	t.Logf("失败上传数：%d", len(errChan))
	t.Logf("总耗时：%v", elapsed)
	t.Logf("QPS：%.2f", float64(successCnt.Load())/elapsed.Seconds())

	// 验证：无错误（文件上传要求更严格）
	if len(errChan) > 0 {
		t.Errorf("Multipart POST高并发测试失败，错误数：%d", len(errChan))
		for err := range errChan {
			t.Error(err)
		}
	} else {
		t.Logf("✅ Multipart POST 10协程高并发测试通过，协程安全，文件上传正常")
	}
}
