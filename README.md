# itdog-api-go

[itdog.cn](https://www.itdog.cn) 测速 API 的 Golang 封装服务，针对上游 **2026-10 API 改版**完全重写：新 WebSocket 协议、点选人机验证自动求解、会话持久化免验证。

## 特性

- **四种测试**：Ping / TCPing / HTTP 测速 / DNS 解析，覆盖 300+ 国内外监测点
- **验证码全自动**：视觉大模型在时间预算内重试解点选验证码（方案 C），可选人工页面兜底
- **一次验证，长期免验证**：上游验证态绑定 PHP 会话，通过后持久化到磁盘 + 定时保活，重启不丢
- **零依赖部署**：单二进制 / Docker 镜像（<20MB），数据只有一个 JSON 会话文件
- **错误可观测**：上游失败返回结构化错误（HTTP 502 + 信封），容器自带 `/healthz` 探活

## 快速开始

### Docker（推荐）

```bash
docker build -t itdog-api .

# 最简运行（验证码用人工页面解，浏览器打开 http://localhost:3000/it/captcha）
docker run -d --name itdog \
  -p 3000:3000 \
  -v itdog-data:/app/data \
  itdog-api

# 全自动运行（配置视觉模型后无需人工介入）
docker run -d --name itdog \
  -p 3000:3000 \
  -v itdog-data:/app/data \
  -e ITDOG_CAPTCHA_VISION_URL="https://你的OpenAI兼容网关/v1" \
  -e ITDOG_CAPTCHA_VISION_KEY="sk-xxx" \
  -e ITDOG_CAPTCHA_VISION_MODEL="视觉模型名（必须支持图片输入）" \
  itdog-api
```

### Docker Compose

```yaml
services:
  itdog:
    build: .
    ports: ["3000:3000"]
    volumes: ["itdog-data:/app/data"]
    environment:
      TZ: Asia/Shanghai
      ITDOG_CAPTCHA_TOKEN: "改成一串随机值"        # 公网部署保护人工页面
      ITDOG_CAPTCHA_VISION_URL: "https://你的网关/v1"
      ITDOG_CAPTCHA_VISION_KEY: "sk-xxx"
      ITDOG_CAPTCHA_VISION_MODEL: "视觉模型名"
    restart: unless-stopped
volumes:
  itdog-data:
```

### 源码运行

```bash
go run ./cmd/server     # 默认 :3000
go test ./...           # 单元测试
```

## API

统一响应信封：`{"code":200,"timestamp":...,"data":{...}}`。**HTTP 状态码与信封 `code` 一致**，失败附 `message`：

- `400` 参数错误，`403` 人工页面令牌错误，`404` 路由不存在，`405` 方法错误，`413` 请求体超过 128 KiB。
- `502` 上游请求、协议或结果通道异常；`503` 任务队列已满或排队超时（附 `Retry-After: 1`）；`504` 请求、验证或结果收集超时。
- `499` 请求取消；客户端已经断开时，该状态主要用于服务端日志。
- `500` 内部错误。每次响应附 `X-Request-ID`，日志包含请求 ID、路径、状态和耗时，不记录查询参数、请求头或请求体。

旧实现曾对错误也返回 HTTP 200；客户端应改为同时处理非 2xx 状态及 JSON 信封。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/healthz` | 探活 |
| GET | `/it/ping?host=` | ICMP Ping，host 为域名或 IPv4 |
| GET | `/it/tcping?host=&port=` | TCP Ping，port 1-65535 |
| POST | `/it/http` | HTTP 测速，JSON body |
| GET | `/it/dns?domain=&type=&server=` | DNS 解析，type: a/cname/mx/aaaa/ns/txt/ptr/srv，server 默认 223.5.5.5 |
| GET | `/it/captcha` | 人工解验证码页面（浏览器打开） |
| GET | `/it/captcha/status` | 当前验证码挑战（图片 base64 + version） |
| POST | `/it/captcha` | 提交人工答案 `{"points":[[x,y],[x,y]],"version":v}` |

### 示例

```bash
# Ping
curl "http://localhost:3000/it/ping?host=baidu.com"

# TCPing
curl "http://localhost:3000/it/tcping?host=baidu.com&port=443"

# HTTP 测速
curl -X POST "http://localhost:3000/it/http" -H "Content-Type: application/json" \
  -d '{"url":"https://www.baidu.com","method":"get"}'

# DNS 解析
curl "http://localhost:3000/it/dns?domain=baidu.com&type=a&server=223.5.5.5"
```

`POST /it/http` 完整 body（`url` 必填，其余可省略）：

- URL 仅接受 `http` / `https`；`method` 为 `get` / `post`。
- `redirect_num` 省略或 `null` 时默认 5，显式 `0` 表示不跟随重定向，允许范围 0–10。
- `http_version`：`auto/http_1_1/http_2/http_3`；`check_mode`：`fast/slow`。
- `dns_server_type`：`isp/custom`；`custom` 必须提供 IP 形式的 `dns_server`。
- Ping/TCPing 主机名只做语法校验，不依赖服务本机 DNS；域名使用 ASCII/Punycode。DNS 查询支持 `_sip._tcp` 等 SRV 名称，无需预先存在 A/AAAA 记录。

```json
{
  "url": "https://example.com",
  "method": "get",
  "cookies": "",
  "ua": "",
  "referer": "",
  "host": "",
  "host_s": "",
  "ipv4": "1.1.1.1",
  "check_mode": "fast",
  "http_version": "auto",
  "redirect_num": 5,
  "dns_server_type": "isp",
  "dns_server": ""
}
```

### 响应结构

```json
{
  "code": 200,
  "timestamp": 1791943596000,
  "data": {
    "complete": true,
    "partial": false,
    "results": [
      {
        "node": "北京7联通",
        "ip": "111.63.65.103",
        "address": "中国/河北/石家庄/移动",
        "delay": "10ms"
      }
    ],
    "total": {
      "全部": {
        "nodes": 1, "success": 1, "failed": 0,
        "all": 10, "avg": "10.00", "argv": "10.00",
        "fast": { "name": "北京7联通", "delay": 10 },
        "slow": { "name": "北京7联通", "delay": 10 }
      }
    }
  }
}
```

示例省略了其他统计分组。所有分组都使用相同口径：`nodes` 为收到的节点数，`success/failed` 为有效/失败样本数；`all` 为有效延迟之和，`avg = all / success`。失败、缺失、负数及非有限延迟不参与平均值和极值；合法 `0ms` 参与。无有效样本时 `avg` 为 `""`，`fast/slow` 为 `{ "name": "", "delay": 0 }`。`argv` 暂作 `avg` 的兼容别名，不再返回倒数。

只有收到上游 `finished` 才设置 `complete=true`；断流、超时或协议异常返回非 2xx。若已经收到节点结果，错误信封保留 `data.results/total`，并设置 `complete=false, partial=true`。`complete` 表示结果流完整，不代表每个节点都测速成功。

HTTP 测速的节点字段：`http_code / all_time / dns_time / connect_time / download_time / redirect / redirect_time / head`；DNS 的节点字段：`delay / result[]`（解析记录列表）。

## 验证码机制

上游 2026-10 起所有任务端点强制点选人机验证。本服务的处理策略：

1. **视觉模型自动求解**：在验证时间预算内重试，1s→15s 递增退避。上游明确拒绝、请求取消或预算耗尽时停止。
2. **人工兜底**：`auto` 模式设置 `ITDOG_CAPTCHA_VISION_TRIES=N` 后，每次模型调用均计次，包括成功返回坐标但上游判错的情况；用尽 N 次后下一次挑战转人工。计数和人工阶段在每个新验证周期开始时重置。`vision` 模式仅使用模型，不转人工。
3. **人工窗口**：单次等待默认 900 秒，同时受验证和请求总预算限制；不存在固定“3 次机会”。提交必须携带当前 `version`，坐标必须位于图片范围内。
4. **会话持久化**：cookie 以 0600 权限原子写入 `data/cookies.json`；保活与任务创建共用会话锁，忙碌时跳过保活，重启后恢复会话。会话是否仍有效由上游决定。

设置令牌后通过 `/it/captcha?token=...` 打开人工页面。页面读取令牌后会从地址栏移除，后续请求使用 `X-Captcha-Token`；同时设置仅限人工接口路径的 HttpOnly / SameSite=Strict 会话 Cookie，支持刷新页面。页面禁用缓存和 Referer 传播。

> 人工模式（不配视觉模型）：验证码出现时打开 `http://localhost:3000/it/captcha` 按顺序点击图中要求的 2 个汉字即可。SSH 隧道 `-L 8080:localhost:3000` 可在服务器部署时使用。

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `HTTP_PORT` | 3000 | 监听端口 |
| `ITDOG_CAPTCHA_SOLVER` | auto | auto / vision / manual / none |
| `ITDOG_CAPTCHA_VISION_URL` | https://api.openai.com/v1 | OpenAI 兼容端点 |
| `ITDOG_CAPTCHA_VISION_KEY` | 空 | API Key |
| `ITDOG_CAPTCHA_VISION_MODEL` | 空 | 视觉模型名，**必须支持图片输入** |
| `ITDOG_CAPTCHA_VISION_TRIES` | 0（不限次数） | auto 模式每验证周期视觉尝试上限；仍受验证时间预算限制 |
| `ITDOG_CAPTCHA_VISION_TIMEOUT` | 30 | 单次视觉调用超时（秒） |
| `ITDOG_CAPTCHA_MANUAL_WAIT` | 900 | 人工等待窗口（秒） |
| `ITDOG_CAPTCHA_TOKEN` | 空 | `/it/captcha*` 访问令牌（公网部署建议设置） |
| `ITDOG_COOKIE_FILE` | data/cookies.json | 会话持久化路径 |
| `ITDOG_SESSION_KEEPALIVE` | 600 | 会话保活间隔（秒，0 关闭） |
| `ITDOG_REQUEST_TIMEOUT` | 1020 | 单请求总预算（秒，含排队、验证和结果收集） |
| `ITDOG_QUEUE_SIZE` | 32 | 最大待创建任务数，另允许一个正在创建的任务 |
| `ITDOG_QUEUE_TIMEOUT` | 30 | 排队最长等待（秒），取消会立即退出队列 |
| `ITDOG_VERIFY_TIMEOUT` | 900 | 单次会话验证总预算（秒） |
| `ITDOG_RESULT_TIMEOUT` | 45 | 结果阶段总预算（秒，含 WebSocket 握手） |

时间配置须为 1–86400 秒（保活额外允许 0），队列大小为 1–10000，视觉尝试次数为 0–10000。非法数值或 solver 名称在启动时直接报错。

HTTP 服务设置了请求头、读取、写入及空闲超时。WebSocket 写入限时 5 秒，首条结果后连续 6 秒无新结果即返回超时；单消息上限 1 MiB、累计结果上限 16 MiB / 4096 条。SIGINT/SIGTERM 会取消进行中的请求与保活，并在最多 10 秒内完成 HTTP 关闭。反向代理超时应配合请求预算设置。

## 上游协议要点（逆向摘要）

- WebSocket：`wss://www.itdog.cn/websockets/{task_id}/{token}`，token = `md5(task_id + "What this is is no longer important.")` 十六进制第 8..24 位
- 点选验证码：`GET /verify/clicaptcha.php?type=ajax` 预检 → `POST ?type=tool`（`do=check&info=x,y-x,y;宽;高;base64轨迹`）提交；校验为包围盒命中，轨迹仅弱校验；验证态绑定 PHPSESSID
- 消息字段：ping/tcping 用 `result`；http 用 `download_time`（无 ssl_time）；dns 用 `time/result[]`
- 旧版 guard/guardret 反爬已下线

完整核对清单见 [docs/compatibility-checklist.md](docs/compatibility-checklist.md)。

## 开发

```bash
go build ./...   # 构建
go vet ./...     # 静态检查
go test -race -cover ./... # 本地 HTTP/WebSocket 模拟测试、并发检测和覆盖率
```

目录结构：

```
cmd/server          # 入口：solver 装配、keepalive、HTTP 服务
internal/itdog      # 上游客户端：任务、队列、WebSocket、会话、验证码及统计
internal/httpapi    # HTTP API 与人工验证码页面
docs/               # 上游兼容性清单
```

## 免责声明

本项目仅供学习与研究用途，请勿用于任何违反 itdog.cn 服务条款或法律法规的场景。验证码自动求解相关能力仅用于接口兼容性研究。
