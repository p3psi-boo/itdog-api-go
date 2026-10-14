# 上游兼容性核对清单（2026-10-14 实测复核）

## 任务创建

- [x] `POST /ping/{host}`：表单 `line=&button_click=yes&dns_server_type=isp&dns_server=`；HTML 含 `var task_id='...'`
- [x] `POST /tcping/{host}:{port}`：同 ping 表单
- [x] `POST /http/`：完整表单（host/host_s/check_mode/http_version/ipv4/method/referer/ua/cookies/redirect_num/dns_server_type/dns_server/line）
- [x] `POST /dns/{domain}`：`dns_type/dns_server_type=custom/dns_server/line`
- [x] 未验证会话的任务 POST 一律返回验证页（全端点强制，实测确认）

## WebSocket 结果通道

- [x] URL：`wss://www.itdog.cn/websockets/{task_id}/{token}`
- [x] token：`md5(task_id + "What this is is no longer important.")` hex 的 `[8:24]`（实测样本回归测试 `TestGetToken`）
- [x] 握手后发送 `{"task_id":"..."}`；收到 `type=finished` 结束
- [x] 消息字段映射：
  - ping/tcping：`ip/result/name/address/node_id/line/region/province`
  - http：`http_code/all_time/dns_time/connect_time/download_time/redirect/redirect_time/head`（注意：无 ssl_time，是 download_time）
  - dns：`time/result[]/host`
- [x] `type=node_error` → 监测点异常（计入超时统计）
- [x] 解析失败（非 IP）节点：js 显示"解析失败"，本服务 result 值为 null/0 归入超时

## 人机验证（上游 2026-10 新增）

- [x] 预检 `GET /verify/clicaptcha.php?type=ajax`：`{type: verify|success, img}`
- [x] 提交 `POST ?type=tool`：`do=check&info={x,y}-{x,y};{w};{h};{base64 payload}`
- [x] base64 轨迹 payload 仅弱校验（伪造 v2 格式实测通过）
- [x] 校验为包围盒命中（源自开源 hooray/clicaptcha-server，itdog 魔改删除了答案 cookie 泄漏）
- [x] 验证状态绑定 PHPSESSID；`user_token` 恒为 delete（死代码）；`bot_verify_limited` 连续失败会触发（实现退避）
- [x] 会话持久化 + keepalive 后不再触发验证

## 已移除的旧协议（本服务不再实现）

- guard/guardret 反爬（`_guard` cookie + XOR 算法）：上游已下线
- 旧 task_token salt `token_20230313000136kwyktxb0tgspm00yo5`：已更换
- 旧 WebSocket 握手协议（发送 `{"task_id","token"}`）：现为 URL 携带 token
