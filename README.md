# itdog-api-go

[itdog.cn](https://www.itdog.cn) 测速 API 的 Golang 封装服务。针对上游 2026-10 API 改版完全重写。

## 上游 2026-10 版协议要点（逆向结论）

- **任务创建**：仍为 `POST /ping/{host}`、`/tcping/{host}:{port}`、`/http/`、`/dns/{domain}` 表单提交，响应 HTML 中 `var task_id='...'`
- **结果通道**：WebSocket `wss://www.itdog.cn/websockets/{task_id}/{token}`
  - `token = md5(task_id + "What this is is no longer important.")` 十六进制的第 **8..24** 位（`md5(x,16)`）
  - 连接后发送 `{"task_id":"..."}`；`many` 模式需每 2s 重发（本服务用 once 语义 + 静默退出）
  - 每条节点消息含 `node_id/name/address/line/region/province` 及结果字段；`type=finished` 结束
- **人机验证（新增）**：任务 POST 前需通过点选验证码
  - 预检：`GET /verify/clicaptcha.php?type=ajax` → `{type: verify, img}` 或 `{type: success}`
  - 提交：`POST /verify/clicaptcha.php?type=tool`，`do=check&info=x,y-x,y;宽;高;base64(轨迹)`
  - 校验是**包围盒命中**（点在目标字 ~30×30px 框内即过）；轨迹 payload 仅弱校验
  - **验证状态绑定 PHP session（服务端）**：通过一次后持续使用即免验证；旧版 guard/guardret 反爬已移除
- 消息字段（实测）：ping/tcping 用 `result`；http 用 `http_code/all_time/dns_time/connect_time/download_time/redirect/redirect_time/head`；dns 用 `time/result[]`

## 运行

```bash
go run ./cmd/server          # 默认 :3000，HTTP_PORT 可改
```

## API

| 方法 | 路径 | 参数 |
|---|---|---|
| GET | `/it/ping?host=` | host：域名或 IPv4 |
| GET | `/it/tcping?host=&port=` | port 1-65535 |
| POST | `/it/http` | JSON body（见下） |
| GET | `/it/dns?domain=&type=&server=` | type: a/cname/mx/aaaa/ns/txt/ptr/srv；server 默认 223.5.5.5 |
| GET | `/it/captcha` | 人工解验证码页面 |
| GET | `/it/captcha/status` | 当前验证码挑战（含 version） |
| POST | `/it/captcha` | `{"points":[[x,y],[x,y]],"version":v}` |

`/it/http` body：

```json
{
  "url": "https://example.com",
  "method": "get",
  "cookies": "", "ua": "", "referer": "",
  "host": "", "host_s": "",
  "ipv4": "1.1.1.1",
  "check_mode": "fast",
  "http_version": "auto",
  "redirect_num": 5,
  "dns_server_type": "isp", "dns_server": ""
}
```

响应统一信封：`{"code":200,"timestamp":...,"data":{...}}`；`data.results[]` 为节点结果，`data.total` 为运营商统计（全部/联通/移动/电信：nodes/all/avg/fast/slow）。

## 验证码策略（方案 C + 人工兜底）

验证码出现时：**视觉模型无限重试**（识别失败/答错都计次，1s→15s 递增退避；仅在上游明确拒绝或 ctx 取消时停止）。显式设置 `ITDOG_CAPTCHA_VISION_TRIES=N` 后恢复"用尽 N 次降级人工页面"的旧行为（`/it/captcha`，15 分钟窗口 × 3 次机会）。验证一次通过后会话持久化到 `data/cookies.json`，配合 10 分钟 keepalive，日常不再触发验证码。

### 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `HTTP_PORT` | 3000 | 监听端口 |
| `ITDOG_CAPTCHA_SOLVER` | auto | auto/vision/manual/none |
| `ITDOG_CAPTCHA_VISION_URL` | https://api.openai.com/v1 | OpenAI 兼容端点 |
| `ITDOG_CAPTCHA_VISION_KEY` | - | API Key |
| `ITDOG_CAPTCHA_VISION_MODEL` | - | **必须支持图片输入**（如 intern-ai/qwen3.8-27b、gpt-4o-mini） |
| `ITDOG_CAPTCHA_VISION_TRIES` | 0（无限） | 视觉尝试上限；设为正整数后用尽降级人工 |
| `ITDOG_CAPTCHA_VISION_TIMEOUT` | 30 | 单次视觉调用超时（秒） |
| `ITDOG_CAPTCHA_MANUAL_WAIT` | 900 | 人工等待窗口（秒） |
| `ITDOG_CAPTCHA_TOKEN` | 空 | `/it/captcha*` 访问令牌（公网部署建议设置） |
| `ITDOG_COOKIE_FILE` | data/cookies.json | 会话持久化路径 |
| `ITDOG_SESSION_KEEPALIVE` | 600 | 会话保活间隔（秒，0 关闭） |

### 部署

```bash
HTTP_PORT=3000 \
ITDOG_CAPTCHA_VISION_URL=https://your-openai-compatible-gateway/v1 \
ITDOG_CAPTCHA_VISION_KEY=sk-xxx \
ITDOG_CAPTCHA_VISION_MODEL=<视觉模型名> \
ITDOG_CAPTCHA_TOKEN=一串随机值 \
go build -o server ./cmd/server && ./server
```

人工兜底入口（vision 连续失败或未配置时）：SSH 隧道或带 token 打开 `https://你的域名/it/captcha?token=...`，按顺序点 2 个字即可。

## 已验证（2026-10-14 实测）

- ✅ ping baidu.com / qq.com：315/313 节点，含海外
- ✅ tcping baidu.com:443：313 节点
- ✅ http https://www.baidu.com：315 节点，http_code/timing/head 齐全
- ✅ dns baidu.com A @223.5.5.5：314 节点解析结果
- ✅ 视觉模型第 4 次尝试解过验证码 → 会话持久化 → 重启后直接创建任务（零验证码）
- ✅ vision 5 次失败自动降级人工（模拟端点故障实测）
