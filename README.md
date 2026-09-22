# WorkBuddy 自动签到（Go）

一个用 Go 编写的 WorkBuddy 每日自动签到小工具：读取本机桌面端登录凭证并自动解密
5.6+ 版本的 `$wbEncrypted` 加密字段，多账号去重后按需续期 `accessToken`，调用官方接口
完成每日签到，并带当日幂等缓存避免重复请求。

- 纯 Go 标准库实现，无第三方依赖。
- 凭证每次运行现场解密，密钥不落盘。
- 支持多账号、Token 自动续期、当日幂等缓存。

## 环境与兼容性

**运行前提**

- Windows 10/11（凭证解密依赖本机安装的 `WorkBuddy.exe`）
- Go 1.24 及以上
- 已登录的 WorkBuddy 桌面端（用于提供凭证）

**兼容性**

| 维度 | 说明 |
| --- | --- |
| 桌面端版本 | `< 5.6`：凭证字段为明文，直接读取；`>= 5.6`：字段为 `$wbEncrypted` 信封，自动解密 |
| 操作系统 | Windows 完整可用并已实测；macOS/Linux 仅能读取明文凭证，无法解密 5.6+ 信封，未实测 |
| 功能范围 | 凭证解析 + 每日签到；不含成长中心、互动玩法、开学季等其它任务 |
| 运行依赖 | 仅 Go 标准库 |

> 解密依赖 `WorkBuddy.exe` 中 `electron_browser_workbuddy_storage` 绑定提供的构建密钥；
> 官方更换绑定名或密钥方案时需同步适配。

## 构建

```powershell
go build -o wbcheckin.exe .
```

## 使用

```powershell
# 默认：发现账号 -> 按需续期 -> 签到（命中当日缓存则跳过）
.\wbcheckin.exe

# 其它用法
.\wbcheckin.exe --dry-run            # 只列出发现的账号，不调用签到接口
.\wbcheckin.exe --force              # 忽略当日缓存，强制重新签到
.\wbcheckin.exe --uid <uid>          # 只处理指定账号（可多次指定）
.\wbcheckin.exe --json               # 以 JSON 输出结果
.\wbcheckin.exe --no-refresh         # 禁用 accessToken 自动续期
.\wbcheckin.exe --auths <dir>        # 解密后凭证目录（默认 ./auths）
.\wbcheckin.exe --cache <file>       # 幂等缓存文件（默认 ./checkin-cache.json）
.\wbcheckin.exe --workbuddy-exe <p>  # 指定 WorkBuddy.exe 路径（解密用）
.\wbcheckin.exe --help               # 查看帮助
```

退出码：

| 退出码 | 含义 |
| --- | --- |
| 0 | 全部成功或今日已签到 |
| 1 | 存在失败账号 |
| 2 | 未发现可用账号或参数错误 |

## 工作原理

### 1. 凭证来源

按以下顺序发现账号，并按 `uid` 去重、保留最近刷新的一份：

- `--auths` 目录下的 `*.json`（本工具回写的明文凭证）；
- 本机桌面端登录文件：
  - Windows：`%LOCALAPPDATA%\CodeBuddyExtension\Data\Public\auth\`
  - macOS：`~/Library/Application Support/CodeBuddyExtension/Data/Public/auth/`
  - Linux：`$XDG_DATA_HOME/CodeBuddyExtension/Data/Public/auth/`

### 2. 加密字段解密

5.6 及以上版本的桌面端会把 `nickname`、`phoneNumber`、`accessToken`、`refreshToken`
等字段包装成 `{"$wbEncrypted":1,"envelope":"..."}`，其中 `envelope` 是 base64 的 JSON
信封，内容为 AES-256-GCM 密文。工具会自动识别是否含加密字段，含则按下述流程解密：

1. 以 `ELECTRON_RUN_AS_NODE=1` 方式运行本机 `WorkBuddy.exe`，通过
   `process._linkedBinding("electron_browser_workbuddy_storage").loggerGet()`
   取出构建密钥 `atRestSecretKey`；
2. `key = SHA256(atRestSecretKey)`，按固定 AAD 规则用 `crypto/aes` + `crypto/cipher`
   对每个加密字段做 AES-256-GCM 解密；
3. 密钥每次运行现场获取且不落盘；若解密失败会强制重新取一次密钥并重试一次。

### 3. 签到

```
POST {endpoint}/v2/billing/meter/checkin-activity-status   # 查询状态
POST {endpoint}/v2/billing/meter/daily-checkin             # 未签到时领取
```

`endpoint` 默认 `https://copilot.tencent.com`，请求头携带 `Authorization: Bearer <accessToken>`
与 `X-User-Id`。

### 4. Token 续期

当 `accessToken` 缺失或距过期不足 7 天时：

```
POST https://copilot.tencent.com/v2/plugin/auth/token/refresh
Header: X-Refresh-Token / X-Auth-Refresh-Source: plugin
```

成功后把新令牌回写到 `--auths` 目录下的 `<uid>.json`（明文）。

## 免责声明

> [!WARNING]
> **请在使用前仔细阅读以下内容。使用本工具即表示你已理解并接受全部条款。**

1. **非官方项目**：本工具为第三方个人逆向研究实现，与 WorkBuddy、CodeBuddy、腾讯及其关联公司**无任何关联**，也未经其授权、认可或背书。
2. **仅供学习研究**：本项目仅用于个人学习、技术研究与交流，**禁止**用于任何商业用途、批量操作、代他人签到或任何牟利行为。
3. **可能违反服务条款**：自动化调用官方接口可能违反对应产品的用户协议或服务条款，存在账号被限流、封禁或产生其它不利后果的风险，**由使用者自行承担**。
4. **接口随时可能失效**：官方接口、加密方案与密钥均可能在不通知的情况下变更，本工具随时可能失效，作者不保证可用性与持续维护。
5. **凭证安全自负**：本工具会在本机读取并在明文目录写入账号凭证（含 `accessToken`/`refreshToken`）。请自行确保运行环境安全，**切勿**将 `auths/`、`checkin-cache.json` 或任何解密后的凭证上传、分享或提交到公开仓库，否则可能导致账号被盗用。
6. **无任何担保**：本软件按“现状”提供，不提供任何明示或暗示的担保。因使用或无法使用本工具造成的任何直接或间接损失（包括但不限于账号异常、数据丢失、积分/权益变动等），作者**概不负责**。
7. **合规责任自负**：请在遵守当地法律法规及相关服务条款的前提下使用；如你不同意上述任何条款，请立即停止使用并删除本项目。

## 许可证

本项目未附带开源许可证。如需使用、分发或二次开发，请先联系作者取得授权。
