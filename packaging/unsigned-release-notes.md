[简体中文](#简体中文) · [English](#english)

> **v0.2 系列代号：Wind Rose**<br>
> **v0.2 series codename: Wind Rose**

## 简体中文

### v0.2.3 主要变化（相对 v0.2.2）

v0.2.2 为 OpenSurge 加入了 Tailscale / Headscale 出站。随着 Tailnet、Exit Node、代理节点和 `DIRECT` 等可选路径增多，v0.2.3 重点让每条连接的设备归属、命中规则和实际出口变得可见，并补齐出口切换后的连接刷新、双栈接管状态与网关运行反馈。

- **新增独立的连接页面**：按 Mac 本机、下游设备或其他来源查看当前活跃连接，展示访问目标、协议、来源地址族、命中规则、完整出口链、实时上下行速率与持续时间；支持按设备、TCP/UDP、IPv4/IPv6 和出口类型筛选，方便确认流量实际使用 Tailscale、Exit Node、代理节点还是 `DIRECT`。
- **更准确的双栈设备归属**：同一设备的 IPv4、IPv6 和 IPv6 隐私地址会聚合显示；Mac 本机流量根据 mihomo 当前实际运行的系统 TUN 地址精确识别，不依赖可能缺失的进程信息，也不会把下游 TUN 流量误判为本机连接。无法可靠确认身份的连接会单独保留在“无法归属”。
- **出口切换后的定向连接刷新**：切换 Mac 本机模式、设备默认出口或策略组选择后，界面会提示新出口只影响新连接，并允许只刷新 Mac 本机或指定设备当前由 OpenSurge 管理的连接，不会一并关闭其他设备的会话；下载、通话等可能中断的影响会在执行前说明。
- **统一的 IPv4 / IPv6 接管状态**：Web GUI 和菜单栏 App 分别展示 IPv4 与 IPv6 是否正在接管，并区分等待上游、已关闭、已停止、异常和重启后待清理等状态，比单独显示底层 forwarding 开关更接近网关的实际运行情况。
- **更清晰的操作与 DHCP 检查反馈**：启动、停止、重载以及路由器 DHCP 关闭/恢复检查会持续展示当前阶段和最终结果；DHCP OFFER 探测期间不再表现为页面无响应，已经关闭或自动消失的操作结果也不会回退显示更早的旧记录。
- **改进 DHCP/DNS 运行稳定性**：修正 dnsmasq 在正式环境中的前台进程管理，并保留正确的日志格式和运行路径，减少进程状态、日志与实际 DHCP/DNS 服务不一致的情况。感谢 [@Kaliscuit](https://github.com/Kaliscuit) 在 [PR #34](https://github.com/YTwsy/OpenSurge-for-Mac/pull/34) 中贡献这项修复。

### 选择安装包

| Mac 类型 | 安装包 | 最低系统 |
| --- | --- | --- |
| Apple Silicon（M1 及更新芯片） | `arm64-unsigned.pkg` | macOS 13+ |
| Intel Mac | `x86_64-unsigned.pkg` | macOS 13+ |

> 安装包未进行 Developer ID 签名或 notarization。正式 Release 会同时提供 `SHA256SUMS` 和 GitHub build provenance，供下载后核验。

### 安装

1. 下载与你的 Mac 芯片匹配的安装包。
2. 双击安装包。如果 macOS 阻止打开，请进入**系统设置 → 隐私与安全性**，选择**仍要打开**并完成身份验证，然后重新打开安装包。
3. 安装完成后，从 `/Applications` 打开 **OpenSurge**。

安装完成后，网关默认保持停止；只有在 OpenSurge 控制面中明确操作后才会启动。

<details>
<summary>可选：校验下载文件</summary>

下载 `SHA256SUMS`，运行 `shasum -a 256 安装包名称`，并与文件中的对应记录比较。

也可以使用 GitHub CLI 核对安装包的构建来源：

```sh
gh attestation verify OpenSurge-for-Mac-*-arm64-unsigned.pkg \
  -R YTwsy/OpenSurge-for-Mac
```

Intel 安装包请将命令中的 `arm64` 替换为 `x86_64`。

</details>

### 许可证

OpenSurge 自有代码采用 `GPL-3.0-only`。第三方许可证、声明与准确的对应源码链接会安装到：

`/Library/Application Support/OpenSurge/share/licenses/`

- OpenSurge-patched Mihomo `1.19.30-opensurge.1` 的上游基线源码：<https://github.com/MetaCubeX/mihomo/tree/ac017cdd246ce8bd547653d927e7bf77d7ee73d5>
- dnsmasq 2.93 源码：<https://thekelleys.org.uk/dnsmasq/dnsmasq-2.93.tar.gz>

---

## English

### v0.2.3 highlights since v0.2.2

v0.2.2 introduced managed Tailscale / Headscale outbound access. With Tailnet resources, Exit Nodes, proxy nodes, and `DIRECT` now available as routing paths, v0.2.3 makes connection ownership, matched rules, and actual egress visible, while completing the workflow around connection refresh, dual-stack takeover status, and gateway operation feedback.

- **A dedicated Connections page:** Inspect current active connections by the local Mac, downstream device, or other source. Each row shows its destination, protocol, source address family, matched rule, complete outbound chain, live transfer rate, and duration. Filters for device, TCP/UDP, IPv4/IPv6, and route type make it easier to confirm whether traffic is actually using Tailscale, an Exit Node, a proxy, or `DIRECT`.
- **More accurate dual-stack ownership:** IPv4, IPv6, and IPv6 privacy addresses belonging to the same device are aggregated together. Local Mac traffic is identified from Mihomo's live system-TUN addresses instead of optional process metadata, preventing downstream TUN traffic from being attributed to the Mac. Connections without reliable identity evidence remain visible under Unclassified.
- **Scoped connection refresh after outlet changes:** After changing the Mac routing mode, a device's default outlet, or a policy-group selection, the UI explains that only new connections use the new outlet and offers to refresh only the Mac or the affected device's OpenSurge-managed connections. Other devices are left untouched, and possible interruptions to downloads or calls are disclosed before refresh.
- **Unified IPv4 and IPv6 takeover status:** The Web GUI and menu bar app now report IPv4 and IPv6 takeover separately, including ready, waiting for upstream, disabled, stopped, failed, and interrupted states. These states describe the effective gateway path more clearly than the underlying forwarding switch alone.
- **Clearer operation and DHCP-check feedback:** Start, stop, reload, and router-DHCP disable/restore checks keep their current phase and final result visible. DHCP OFFER probes no longer appear as an unresponsive page, and dismissing or expiring the newest result no longer reveals an older operation record.
- **More reliable DHCP/DNS process handling:** Dnsmasq now uses the correct production foreground mode while preserving its log format and runtime paths, reducing disagreement between process state, logs, and the effective DHCP/DNS service. Thanks to [@Kaliscuit](https://github.com/Kaliscuit) for contributing this fix in [PR #34](https://github.com/YTwsy/OpenSurge-for-Mac/pull/34).

### Choose a package

| Mac | Package | Minimum system |
| --- | --- | --- |
| Apple Silicon (M1 or newer) | `arm64-unsigned.pkg` | macOS 13+ |
| Intel Mac | `x86_64-unsigned.pkg` | macOS 13+ |

> The installers are not Developer ID signed or notarized. The stable Release will also provide `SHA256SUMS` and GitHub build provenance for post-download verification.

### Install

1. Download the package matching your Mac.
2. Double-click the package. If macOS blocks it, open **System Settings → Privacy & Security**, choose **Open Anyway**, authenticate, and reopen the package.
3. After installation, open **OpenSurge** from `/Applications`.

The gateway remains stopped after installation and starts only when explicitly requested from the OpenSurge control plane.

<details>
<summary>Optional: verify the download</summary>

Download `SHA256SUMS`, run `shasum -a 256 PACKAGE_NAME`, and compare the result with the corresponding entry.

You can also verify the package's GitHub build provenance:

```sh
gh attestation verify OpenSurge-for-Mac-*-arm64-unsigned.pkg \
  -R YTwsy/OpenSurge-for-Mac
```

For the Intel package, replace `arm64` with `x86_64`.

</details>

### License

OpenSurge original code is licensed under `GPL-3.0-only`. Third-party license texts, notices, and exact corresponding-source links are installed under:

`/Library/Application Support/OpenSurge/share/licenses/`

- Upstream baseline source for OpenSurge-patched Mihomo `1.19.30-opensurge.1`: <https://github.com/MetaCubeX/mihomo/tree/ac017cdd246ce8bd547653d927e7bf77d7ee73d5>
- dnsmasq 2.93 source: <https://thekelleys.org.uk/dnsmasq/dnsmasq-2.93.tar.gz>
