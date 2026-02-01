# gather - 高性能HTTP采集包
gather 是一个适配慢/快响应网站的HTTP数据采集包，内置灵活的超时配置、自动化Cookie管理、代理支持和连接池优化，专为网页数据采集场景设计。

## 特性
- 🚀 慢/快连接一键切换：默认适配慢响应网站，可快速切换至快速失败模式
- ⏱️ 智能超时管理：支持总超时自动推导细分超时，全局兜底超时优先级最高
- 🔌 灵活代理支持：兼容普通代理/带认证代理，支持动态切换
- 📦 连接池优化：合理的空闲连接管理，平衡性能与资源占用
- 🍪 自动化Cookie：内置CookieJar，自动管理Cookie生命周期

## 快速开始
### 1. 安装
```bash
go get -u github.com/yudeguang17/gather
```
### 2. 实例化采集器
### 场景 1：默认配置（慢速连接，无需手动调用 UseSlowConnConfig）
-  包初始化 (init) 时已自动调用UseSlowConnConfig()，默认适配慢响应网站（总超时 10 分钟），无需重复调用：
```go
package main

import (
   "github.com/yudeguang17/gather"
   "time"
)

func main() {
   // 直接创建采集器，默认启用慢速配置
   // 参数1：UA类型（支持chrome/baidu/google/bing/360/ie等）
   // 参数2：是否开启Cookie日志（调试用）
   ga := gather.NewGather("chrome", false)

   // 设置全局兜底超时（优先级最高，建议与默认总超时一致）
   ga.Client.Timeout = 10 * time.Minute
}
```
### 场景 2：快速连接配置
手动切换至快速配置（总超时 30 秒），适配快速响应网站 / 需要快速失败的场景：
```go
package main

import (
"github.com/yudeguang17/gather"
"time"
)

func main() {
// 切换到快速连接配置
gather.UseFastConnConfig()

// 创建采集器
ga := gather.NewGather("chrome", false)

// 设置全局兜底超时（与快速配置总超时一致）
ga.Client.Timeout = 30 * time.Second
}
```
### 场景 3：自定义总超时配置
根据业务需求自定义总超时，自动推导各阶段细分超时：
```go
package main

import (
   "github.com/yudeguang17/gather"
   "time"
)

func main() {
   // 自定义总超时：10秒，快连接模式，跳过TLS证书验证
   // 参数1：总超时时间
   // 参数2：是否为慢连接场景（true=慢连接，false=快连接）
   // 参数3：是否跳过TLS证书验证
   gather.SetGatherConfigByClientTimeout(10*time.Second, false, true)

   // 创建采集器
   ga := gather.NewGather("chrome", false)

   // 设置全局兜底超时（与自定义总超时一致）
   ga.Client.Timeout = 10 * time.Second
}
```
### 3. 基础使用：GET 请求示例
```go
package main

import (
   "fmt"
   "github.com/yudeguang17/gather"
   "io/ioutil"
   "time"
)

func main() {
   // 1. 默认慢速配置创建采集器
   ga := gather.NewGather("chrome", false)
   ga.Client.Timeout = 10 * time.Minute

   // 2. 发送GET请求
   resp, err := ga.Client.Get("https://example.com")
   if err != nil {
      fmt.Printf("请求失败：%v\n", err)
      return
   }
   defer resp.Body.Close()

   // 3. 读取响应内容
   body, err := ioutil.ReadAll(resp.Body)
   if err != nil {
      fmt.Printf("读取响应失败：%v\n", err)
      return
   }

   // 4. 输出结果
   fmt.Printf("响应状态码：%d\n", resp.StatusCode)
   fmt.Printf("响应内容：%s\n", string(body))
}
```
### 4. 高级使用：连接池优化示例
   基于 gather 内置的 Pool 对象池实现高并发采集，复用实例、控制并发资源上限：
```go
package main

import (
   "fmt"
   "github.com/yudeguang17/gather"
   "sync"
)

func main() {
   // 1. 配置请求头
   headers := map[string]string{
      "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
      "Accept":     "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
   }
   // 2. 初始化Pool连接池
   // 参数说明：
   // headers: 请求头配置
   // proxyURL: 代理地址（无代理传空字符串）
   // timeOut: 单次请求超时时间(秒)
   // isCookieLogOpen: 是否开启Cookie日志
   // num: 池大小（实例数量）
   pool := gather.NewGatherUtilPool(headers, "", 30, false, 10)

   // 3. 高并发采集示例
   var wg sync.WaitGroup
   urls := []string{
      "https://example.com/page1",
      "https://example.com/page2",
      "https://example.com/page3",
      "https://example.com/page4",
      "https://example.com/page5",
   }

   for _, url := range urls {
      wg.Add(1)
      go func(u string) {
         defer wg.Done()
         // 使用Pool的Get方法（无Cookie）
         html, redirectURL, err := pool.Get(u, "")
         if err != nil {
            fmt.Printf("采集%s失败：%v\n", u, err)
            return
         }
         fmt.Printf("采集%s成功，重定向地址：%s，响应长度：%d\n", u, redirectURL, len(html))
      }(url)
   }

   wg.Wait()
   fmt.Println("所有采集任务完成")

   // 4. 带Cookie的请求示例
   cookies := "session=xxx; token=yyy"
   html, _, err := pool.GetUtil("https://example.com/secure", "", cookies)
   if err != nil {
      fmt.Printf("带Cookie采集失败：%v\n", err)
   } else {
      fmt.Printf("带Cookie采集成功，响应长度：%d\n", len(html))
   }

   // 5. POST请求示例
   postMap := map[string]string{
      "username": "test",
      "password": "123456",
   }
   htmlPost, _, err := pool.Post("https://example.com/login", "", postMap)
   if err != nil {
      fmt.Printf("POST请求失败：%v\n", err)
   } else {
      fmt.Printf("POST请求成功，响应长度：%d\n", len(htmlPost))
   }
}
```
## 核心配置说明
| 配置方式                | 适用场景                          | 核心特点                                  |
|-------------------------|-----------------------------------|-------------------------------------------|
| 默认配置（慢速）        | 慢响应网站、动态生成页面、海外网站 | ResponseHeaderTimeout=0（无限等响应头），总超时 10 分钟 |
| 快速配置                | 静态页面、内网服务、高性能 API     | 所有超时收紧至 1~5 秒，总超时 30 秒，快速失败        |
| 自定义总超时            | 个性化采集需求                    | 按阶段占比自动分配细分超时，保证总和≤总超时        |
| Pool 连接池默认配置      | 通用高并发采集场景                | 池大小 10，超时 30 秒，信号量启用，自动适配连接数      |
| Pool 连接池自定义配置    | 精细化资源控制场景                | 可自定义最大空闲连接、重试间隔、池上限等参数        |

## 关键注意事项
1. `ga.Client.Timeout` 是全局兜底超时，优先级最高，所有阶段超时总和不会突破此值；
2. 慢连接场景下 `ResponseHeaderTimeout` 会自动设为 0（无限等待响应头），靠 `Client.Timeout` 兜底；
3. Pool 连接池的`num`参数代表实例数量，建议根据业务并发量设置，默认上限 100；
4. Pool 的`GetUtil`/`PostUtil`方法支持自定义 Cookie，适合需要登录态的采集场景；
5. 高并发采集时，Pool 内置信号量控制，无需额外限制并发数，避免连接池过载；
6. 带认证代理可在初始化 Pool 时传入代理地址（第二个参数），格式如`http://user:pass@proxy:port`。