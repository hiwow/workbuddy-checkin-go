# WorkBuddy 自动签到（Go）

一个用 Go 编写的 WorkBuddy 每日自动签到小工具：

- 读取本机 WorkBuddy 桌面端登录凭证，自动解密新版 `$wbEncrypted` 加密字段；
- 多账号自动发现、按 `uid` 去重（同 uid 取最近刷新的一份）；
- `accessToken` 临近过期时自动用 `refreshToken` 续期并回写；
- 调用官方签到接口完成每日签到，并带当日幂等缓存，避免重复请求。

实现仅依赖 Go 标准库，无第三方依赖。

## 环境要求

- 操作系统：Windows（凭证解密依赖本机安装的 WorkBuddy 桌面端）
- Go 1.24 及以上
- 已登录的 WorkBuddy 桌面端（用于提供凭证）

> 非 Windows 环境无法解密新版加密凭证，只能读取 `--auths` 目录下的明文凭证文件。

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

新版桌面端会把 `nickname`、`phoneNumber`、`accessToken`、`refreshToken` 等字段包装成
`{"$wbEncrypted":1,"envelope":"..."}`，其中 `envelope` 是 base64 的 JSON 信封，内容为
AES-256-GCM 密文。解密流程：

1. 以 `ELECTRON_RUN_AS_NODE=1` 方式运行本机 `WorkBuddy.exe`，通过
   `process._linkedBinding("electron_browser_workbuddy_storage").loggerGet()`
   取出构建密钥 `atRestSecretKey`；
2. `key = SHA256(atRestSecretKey)`，按固定 AAD 规则用 `crypto/aes` + `crypto/cipher`
   对每个加密字段做 AES-256-GCM 解密；
3. 密钥每次运行现场获取且不落盘；若解密失败会强制重新取一次密钥重试一次。

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

## 已知限制

- 解密依赖 `WorkBuddy.exe` 中 `electron_browser_workbuddy_storage` 绑定所提供的构建密钥；官方更换绑定名或密钥方案时需同步适配。
- 目前仅覆盖“凭证解密 + 每日签到”，不含成长中心、互动玩法、开学季等其它任务。
- 仅在 Windows 上验证过完整流程。

## 许可证

本项目未附带开源许可证。如需使用、分发或二次开发，请先联系作者取得授权。
