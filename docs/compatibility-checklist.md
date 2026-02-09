# itdog-api Golang 兼容性核对清单

对照基准：`refs/itdog-api`（NestJS 版本）

## 全局行为

| 项目 | 参考实现 | Go 实现 | 结论 |
|---|---|---|---|
| 基础前缀 | `/it` | `/it` | 一致 |
| 成功响应包装 | `{code:200,timestamp,data}` | `{code:200,timestamp,data}` | 一致 |
| 异常响应包装 | HTTP 200 + `{code,message,timestamp}` | HTTP 200 + `{code,message,timestamp}` | 一致 |
| 请求日志 | `[METHOD] originalUrl - ua` | `[METHOD] URL - ua` | 基本一致 |
| 端口配置 | `HTTP_PORT`，默认 `3000` | `HTTP_PORT`，默认 `3000` | 一致 |

## 接口级字段核对

### `GET /it/ping`

| 维度 | 参考实现 | Go 实现 | 结论 |
|---|---|---|---|
| 入参 | `host`（必填，IPv4 或可解析域名） | 同 | 一致 |
| 错误文案 | `不正确的主机名或IPV4地址！` | 同 | 一致 |
| 结果字段 | `ip, delay, node, address` | 同 | 一致 |
| 统计字段 | `total` 来自 `getStatisticians` | 同源迁移 | 一致 |

### `GET /it/tcping`

| 维度 | 参考实现 | Go 实现 | 结论 |
|---|---|---|---|
| 入参 | `host` + `port`（1..65535） | 同 | 一致 |
| 错误文案 | host/port 文案固定中文 | 同 | 一致 |
| 结果字段 | `ip, delay, node, address, port` | 同 | 一致 |
| 统计字段 | `total` 来自 `getStatisticians` | 同源迁移 | 一致 |

### `POST /it/http`

| 维度 | 参考实现 | Go 实现 | 结论 |
|---|---|---|---|
| 入参 | body: `url` 必填，`method` 可选（仅校验 `get/post`） | body: `url` 必填，`method` 可选（仅校验 `get/post`） | 一致 |
| 错误文案 | `无效的请求链接！` / `无效的请求方式！` | 同 | 一致 |
| 上游构造 | 固定补充 `check_mode=fast, host=url, host_s=uri.host` | 同 | 一致 |
| 结果字段 | `address,node,ip,http_code,redirect_time,redirect,ssl_time,dns_time,connect_time,all_time,head` | 同 | 一致 |
| 统计延迟键 | `all_time` | `all_time` | 一致 |

### `GET /it/dns`

| 维度 | 参考实现 | Go 实现 | 结论 |
|---|---|---|---|
| 入参 | `domain`,`type`,`server`（默认 `223.5.5.5`） | 同 | 一致 |
| `type` 白名单 | `a,cname,mx,aaaa,ns,txt,ptr,srv` | 同 | 一致 |
| 错误文案 | `无效的解析类型！` | 同 | 一致 |
| 结果字段 | `node, delay, result`；失败时 `delay=-1ms` | 同 | 一致 |
| 统计延迟键 | `time` | `time` | 一致 |

## 上游交互核对（itdog.cn）

| 维度 | 参考实现 | Go 实现 | 结论 |
|---|---|---|---|
| 任务创建 | POST 表单并正则提取 `task_id` | 同 | 一致 |
| 反爬处理 | `guard/guardret` + cookie 刷新重试 | 同 | 一致 |
| websocket 地址 | `wss://www.itdog.cn/websockets` | 同 | 一致 |
| token 规则 | `md5(task_id+salt).slice(8,24)` | 同 | 一致 |

## 当前待修正项

无。
