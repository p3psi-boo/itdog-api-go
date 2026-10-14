# itdog-api-go

[itdog.cn](https://www.itdog.cn) 测速 API 的 Golang 封装服务，针对上游 **2026-10 API 改版**完全重写：新 WebSocket 协议、点选人机验证自动求解、会话持久化免验证。

## 特性

- **四种测试**：Ping / TCPing / HTTP 测速 / DNS 解析，覆盖 300+ 国内外监测点
- **验证码全自动**：视觉大模型无限重试解点选验证码（方案 C），可选人工页面兜底
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

统一响应信封：`{"code":200,"timestamp":...,"data":{...}}`；失败时 `code` 为 400/403/502 等，附 `message`。

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

`POST /it/http` 完整 body（均可省略）：

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
    "results": [
      {
        "node": "北京7联通",
        "ip": "111.63.65.103",
        "address": "中国/河北/石家庄/移动",
        "delay": "10ms"
      }
    ],
    "total": {
      "全部":   { "nodes": 315, "all": 19639, "avg": "62.35", "fast": {...}, "slow": {...} },
      "联通": { "nodes": 90,  "...": "..." },
      "移动": { "nodes": 95,  "...": "..." },
      "电信": { "nodes": 85,  "...": "..." }
    }
  }
}
```

HTTP 测速的节点字段：`http_code / all_time / dns_time / connect_time / download_time / redirect / redirect_time / head`；DNS 的节点字段：`delay / result[]`（解析记录列表）。

## 验证码机制

上游 2026-10 起所有任务端点强制点选人机验证。本服务的处理策略：

1. **视觉模型自动求解**（默认）：无限重试，识别失败/答错均计次，1s→15s 递增退避；仅在上游明确拒绝或请求取消时停止
2. **人工兜底**（可选）：设置 `ITDOG_CAPTCHA_VISION_TRIES=N` 后，视觉用尽 N 次自动切换人工页面（`/it/captcha`，15 分钟窗口 × 3 次机会，公网用 `ITDOG_CAPTCHA_TOKEN` 保护）
3. **会话持久化**：验证一次通过后 cookie 写入 `data/cookies.json`（容器挂 `/app/data` 卷），配合每 10 分钟保活，日常不再触发验证码；重启自动恢复

> 人工模式（不配视觉模型）：验证码出现时打开 `http://localhost:3000/it/captcha` 按顺序点击图中要求的 2 个汉字即可。SSH 隧道 `-L 8080:localhost:3000` 可在服务器部署时使用。

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `HTTP_PORT` | 3000 | 监听端口 |
| `ITDOG_CAPTCHA_SOLVER` | auto | auto / vision / manual / none |
| `ITDOG_CAPTCHA_VISION_URL` | https://api.openai.com/v1 | OpenAI 兼容端点 |
| `ITDOG_CAPTCHA_VISION_KEY` | 空 | API Key |
| `ITDOG_CAPTCHA_VISION_MODEL` | 空 | 视觉模型名，**必须支持图片输入** |
| `ITDOG_CAPTCHA_VISION_TRIES` | 0（无限） | 视觉尝试上限；设正整数后用尽降级人工 |
| `ITDOG_CAPTCHA_VISION_TIMEOUT` | 30 | 单次视觉调用超时（秒） |
| `ITDOG_CAPTCHA_MANUAL_WAIT` | 900 | 人工等待窗口（秒） |
| `ITDOG_CAPTCHA_TOKEN` | 空 | `/it/captcha*` 访问令牌（公网部署建议设置） |
| `ITDOG_COOKIE_FILE` | data/cookies.json | 会话持久化路径 |
| `ITDOG_SESSION_KEEPALIVE` | 600 | 会话保活间隔（秒，0 关闭） |

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
go test ./...    # 测试（含 token 算法回归、验证码降级策略）
```

目录结构：

```
cmd/server          # 入口：solver 装配、keepalive、HTTP 服务
internal/itdog      # 上游客户端：任务创建、WebSocket、验证码协议
internal/httpapi    # HTTP API 与人工验证码页面
docs/               # 上游兼容性清单
```

## 免责声明

本项目仅供学习与研究用途，请勿用于任何违反 itdog.cn 服务条款或法律法规的场景。验证码自动求解相关能力仅用于接口兼容性研究。
