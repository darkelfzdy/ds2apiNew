# Mihomo 代理桥（一号一 IP）

ds2api 内置的 Mihomo 代理桥可以把机场订阅节点转换为本地独立代理端口，并把
每个 DeepSeek 账号绑定到独占节点出口，实现"一个账号一个 IP"的防连坐效果。

- 子进程管理：ds2api 启动时自动拉起 `mihomo` / `mihomo.exe`，退出时自动回收。
- 订阅解析：支持 Clash/Mihomo YAML 订阅、Base64 整体编码订阅，以及按行分享链接
  （`vmess://` `ss://` `trojan://` `vless://` `hysteria2://` 等）。
- 账号 ↔ 节点绑定：为每个被绑定的节点分配独立本地 SOCKS5 端口
  （默认从 `127.0.0.1:10801` 起递增），账号的上游请求经对应端口发出。

## 工作原理

代理桥不新增请求路径，而是复用 ds2api 既有的"代理 + 账号 proxy_id"分流机制：

1. 添加订阅后，解析出的节点保存在 `config.mihomo.subscriptions[].nodes`。
2. 绑定账号时，系统为该节点分配本地端口（持久化在 `config.mihomo.port_map`），
   并自动创建一个指向 `127.0.0.1:<port>` 的**托管代理**
   （ID 形如 `mihomo-<hash>`，可在"代理 IP"页看到，请勿手改），
   然后把账号的 `proxy_id` 指向它。
3. 系统重新生成 `data/mihomo/runtime.yaml` 并重启 mihomo：每个活跃绑定对应一个
   `listeners` 入站（`type: socks` + `proxy: <节点名>`），该端口的全部流量直出
   所绑节点，跳过规则匹配。
4. ds2api 处理该账号的请求时，走既有的按账号 SOCKS5 拨号路径，经
   `127.0.0.1:<port>` 从绑定节点出口访问 DeepSeek——不同账号互不共享出口 IP。
5. 解绑、删订阅、节点从订阅中消失时，系统自动回收端口分配、删除无引用的
   托管代理、清理悬空绑定（GC）。

因为分流发生在统一的 DeepSeek client 层，OpenAI / Claude / Gemini / Ollama
等所有协议入口自动继承该能力，无需分别适配。

## 快速开始

1. 下载 mihomo（Clash.Meta 内核）：<https://github.com/MetaCubeX/mihomo/releases>
   - Windows：`mihomo-windows-amd64-*.zip` 解压后重命名为 `mihomo.exe`
   - Linux/macOS：重命名为 `mihomo` 并 `chmod +x`
2. 放置到以下任一位置（按此顺序探测）：
   - 配置项 `mihomo.binary_path` 或环境变量 `MIHOMO_PATH` 指定的路径
   - ds2api 可执行文件同目录 / 当前工作目录
   - 上述目录的 `bin/` 子目录
   - 系统 `PATH`
3. 启动 ds2api，登录管理后台，进入左侧 **代理桥** 页签：
   - 打开"启用代理桥"，保存设置（端口默认值通常无需修改）
   - 添加机场订阅链接，等待解析出节点
   - 在"节点与账号绑定"中为每个账号选择独占节点
4. 绑定成功后，账号请求即经由 `127.0.0.1:<分配端口>` 从对应节点出口发出。

## 配置参考

`config.json` 中的 `mihomo` 节（全部可选，推荐在 WebUI 中维护）：

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `enabled` | `false` | 是否启用代理桥（启动时拉起子进程） |
| `binary_path` | 空 | mihomo 二进制路径，留空自动探测 |
| `base_port` | `10801` | 节点本地端口分配起始值（已有分配时禁止修改） |
| `api_port` | `19090` | mihomo external-controller 监听端口（就绪探测用） |
| `subscriptions` | `[]` | 订阅列表（含解析后的节点缓存），由系统维护 |
| `port_map` | `{}` | 节点键 → 本地端口的持久化分配，由系统维护 |

环境变量：

- `MIHOMO_PATH`：等价于 `binary_path`，优先级低于配置项
- `DS2API_MIHOMO_DIR`：运行目录（默认 `./data/mihomo`），存放生成的
  `runtime.yaml`、`mihomo.log` 与 mihomo 缓存

## 管理 API

均挂在管理员鉴权之下（前缀 `/admin`）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/admin/mihomo/status` | 子进程状态、监听列表、最近错误 |
| PUT | `/admin/mihomo/settings` | 更新 enabled/binary_path/base_port/api_port 并应用 |
| POST | `/admin/mihomo/apply` | 重新生成配置并重启 mihomo |
| GET/POST | `/admin/mihomo/subscriptions` | 订阅列表 / 添加订阅（抓取并解析） |
| POST | `/admin/mihomo/subscriptions/{id}/refresh` | 重新抓取订阅，同步节点与绑定 |
| DELETE | `/admin/mihomo/subscriptions/{id}` | 删除订阅并解除相关绑定 |
| GET | `/admin/mihomo/nodes` | 全部节点及其端口、已绑账号 |
| PUT | `/admin/mihomo/bindings/{identifier}` | 绑定/解绑，body `{"node": "<nodeKey 或空串>"}` |

## 注意事项

- **Vercel 等 serverless 部署不支持**拉起子进程：状态接口返回
  `supported: false`，页面仅展示只读状态；配置仍可编辑，回到常驻进程
  部署后生效。
- 托管代理由系统增删，请勿在"代理 IP"页手动修改或删除；解除绑定请到
  "代理桥"页操作。
- 同一节点可绑定多个账号（共享同一本地端口与出口 IP）；要严格"一号一 IP"，
  请为每个账号选择不同节点。
- mihomo 子进程日志写在 `data/mihomo/mihomo.log`；应用失败时 WebUI 状态卡片
  会展示最近错误（端口被占用、二进制缺失、配置非法等）。
