# wild-view

> 账号额度与模型倍率查看器。由 [wild-work](https://github.com/xxhhlk/wild-work) 派生，
> 保留其账号登录、凭据续期与渠道查询实现，移除全部会话转发能力。

## 定位

- **只读查看**：汇总各平台账号的积分 / Token / 配额窗口、到期时间与模型倍率。
- **不转发会话**：不提供任何 OpenAI / Anthropic 兼容端点，`/v1/*` 一律 404。
- **不产生对话**：已移除聊天保活；依赖对话才能完成的签到（WorkBuddyAI、智谱清言）不参与签到。
- **纯签到可开关**：默认关闭，可在设置中启用，仅在配置时间执行不依赖对话的签到。

## 功能

- **账号管理**：各渠道登录、重新登录、本机客户端凭据导入、昵称、删除。
- **额度查看**：积分 / Token / 窗口百分比按平台分别汇总，附额度明细与精确到期时间；
  缺失、失败、真实零值分开显示，不用其他账号数据替代。
- **模型与倍率**：上游目录、倍率、促销说明、上下文与能力；按账号分别查询并保存，
  同一模型在不同账号倍率不同时逐账号展示；字段缺失不显示为免费。
- **余额变化**：保留积分流水（入项 / 消耗 / 到期），不把套餐到期或校正计为对话消耗。
- **缓存与时效**：额度、目录、倍率各自记录最近成功时间与错误，刷新失败保留上次成功结果。
- **面板与托盘**：浏览器管理面板、系统托盘、开机自启、管理密码与监听地址设置。
- **跨平台**：Windows（完整支持）、macOS、Linux（`--no-tray` 无头运行）。

## 与 wild-work 的差异

| 项目 | wild-work | wild-view |
|---|---|---|
| 会话转发 | OpenAI / Responses / Anthropic / 生图 | 已移除，`/v1/*` 返回 404 |
| 聊天保活 | 定时对话维持会话 | 已移除 |
| 签到 | 全渠道自动签到 | 纯签到，默认关闭，可开关 |
| 运行统计 | 请求数 / Token / TTFB / 吞吐 | 仅保留余额变化 |
| 默认端口 | 7863 | 7864（可与原程序并存） |
| 数据目录 | `WILDWORK_HOME` | `WILDVIEW_HOME` |

数据目录独立：默认为可执行文件旁的 `wild-view-data/`（不与 wild-work 的 `config.json` /
`auths/` / `data/` 重名），也可用 `WILDVIEW_HOME` 指定。两个程序不应并发管理同一份复制凭据
（refresh token 会互相轮换失效）。

## 渠道与接入方式


下表为各渠道的账号接入方式与凭据来源。查看器只读取额度与模型目录，不发起对话。

| 渠道 | 标识 | 接入方式 | 说明 |
|------|----------|----------|------|
| WorkBuddyCN（CodeBuddy） | `workbuddy/*` | 浏览器登录 | WorkBuddy 国内版 |
| WorkBuddyAI | `workbuddyai/*` | 浏览器登录 | WorkBuddy 国际版，与国内版完全独立 |
| TraeWork | `traework/*` | 浏览器登录 | |
| TraeCode | `traecode/*` | 复用 TraeWork 账号 | Trae 代码版（`function=solo_agent`），与 TraeWork 同一上游、共用账号 |
| QoderCN | `qodercn/*` | 浏览器登录 | |
| QoderCOM | `qodercom/*` | 浏览器登录 | Qoder 国际版 |
| 千问办公 | `qwenwork/*` | 浏览器登录 | |
| MonkeyCode | `monkeycode/*` | 从客户端导入 | 平台托管模型 |
| 小浣熊 | `raccoon/*` | 网页版自动登录（CDP）／从客户端导入 | 两条添加路径：主按钮拉起独立 profile 浏览器登录后经 CDP 捕获凭据（跨平台），次按钮复用本机客户端凭据 |
| Loomy（讯飞） | `loomy/*` | 从客户端导入 | 导入型渠道：凭据从**本机已登录的官方客户端**读取；上游无 refresh 端点，session 约 14 天，到期需重新导入 |
| 智谱清言 | `glm/*` | CDP 自动捕获 | **网页版私有接口**（非开放平台），凭据是浏览器 Cookie 里的 `chatglm_refresh_token`；新账号须在智谱清言 App 登录一次才发放初始额度 |
| 华为云果办 OfficeAce | `officeace/*` | 自助 OAuth／从客户端导入 | 华为云 AgentArts 模型网关；两条添加路径：主按钮自助 OAuth（华为云授权码 + PKCE + DPoP，跨平台），次按钮读本机 OfficeAce 桌面端凭据（临时 AK/SK 经 DPAPI 解密，仅 Windows）；对话走不过期的网关 Basic 凭据，每日签到的临时凭据约 2 小时有效并自动续期 |
| MiniMax Code | `minimax/*` | 设备码授权登录 | |
| LobsterAI（有道龙虾） | `lobsterai/*` | 本机回调授权登录 | |
| ZCode（智谱） | `zcode/*` | 设备授权登录 | |
| Gemini | `gemini/*` | 本机回调授权登录 | Google Code Assist |
| CodeArts（华为云） | `codearts/*` | PKCE 授权登录 | |
| 旧 Qoder | `qoder/*` | — | 路由保留，已从界面下线 |

## 项目渊源

本项目最初算法来源于 [Sliverkiss/](https://github.com/Sliverkiss/) 大佬的 xxx2api 系列项目，本项目针对多渠道进行了聚合，针对 Windows 环境进行了适配，降低了使用门槛，并提供跨平台 Web 管理界面。

### 声明

**本项目只是各上游渠道的集成与聚合，本身不涉及任何逆向分析和破解工作。**

即：本仓库不产出、不包含、不分发任何协议逆向研究成果。各渠道的接口参数、签名算法、风控指纹等底层知识，全部来自社区其他开源项目的公开成果；本项目所做的是**在既有公开成果之上做多渠道路由、账号池调度、协议适配与界面封装**，并提供跨平台、低门槛的使用形态。所有逆向相关的功劳与风险归于下述各上游项目作者。

### 致谢

感谢以下开源项目及其作者。它们分别完成了各渠道的接口逆向与协议封装，是本项目各渠道能够落地的前提：

| 项目 | 贡献 |
|------|------|
| [Sliverkiss/workbuddy2api](https://github.com/Sliverkiss/workbuddy2api) | WorkBuddy(CodeBuddy) 国内版接口逆向，本项目最初的算法来源 |
| [Sliverkiss/qoderwork2api](https://github.com/Sliverkiss/qoderwork2api) | QoderWork 接口逆向，Qoder 渠道参考 |
| [wicm84266964/Buddy2api](https://github.com/wicm84266964/Buddy2api) | 千问办公渠道逆向参考 |
| [wpy030414/xrl-router-plugin-qwenwork](https://github.com/wpy030414/xrl-router-plugin-qwenwork) | 千问办公渠道逆向参考 |
| [iceloon/dsh-workbuddyai-connect](https://github.com/iceloon/dsh-workbuddyai-connect) | WorkBuddy 国际版接口逆向，国际版渠道主要参考 |
| [corrinehu/dsh-workbuddy-connect](https://github.com/corrinehu/dsh-workbuddy-connect) | WorkBuddy 渠道架构参考（MIT） |
| [Zhengyuuuui/qoder2api](https://github.com/Zhengyuuuui/qoder2api) | Qoder 渠道端点与逻辑比对参考 |
| [icebears111/qoderwork2api](https://github.com/icebears111/qoderwork2api) | Qoder 渠道端点与逻辑比对参考 |
| [zhangdailin/Orchids-2api](https://github.com/zhangdailin/Orchids-2api) | Qoder 渠道端点与逻辑比对参考 |
| [jasonxu114514/opencode2api](https://github.com/jasonxu114514/opencode2api) | OpenCodeZen（oczen）匿名渠道 endpoint 特殊要求分析 |
| [FishBottle7/opencode2dsh](https://github.com/FishBottle7/opencode2dsh) | 同上，OpenCode 相关补充参考 |
| [ttales430/glm2api](https://github.com/ttales430/glm2api) | 智谱清言（chatglm.cn）网页版私有接口协议逆向，glm 渠道协议依据 |
| [codkeep/MonkeyCodeReverseEngineer](https://github.com/codkeep/MonkeyCodeReverseEngineer) | MonkeyCode 渠道协议对照参考（Apache-2.0） |
| [xxhhlk/raccoon2api](https://github.com/xxhhlk/raccoon2api) | 小浣熊渠道协议逆向（MIT），raccoon 渠道依据 |
| [xxhhlk/loomy2api](https://github.com/xxhhlk/loomy2api) | 讯飞 Loomy 渠道协议逆向（MIT），loomy 渠道依据 |
| [Q-shuang-dot/officeace2api](https://github.com/Q-shuang-dot/officeace2api) | 华为云果办 OfficeAce 渠道协议依据（MIT）——V4/V11 华为云签名（`src/quota.mjs`）、网关错误码分类（`src/server.mjs`）、`max_tokens` 推理预算常量（`src/config.mjs`）、桌面端凭据发现与 DPAPI/AES-256-GCM 档案解密（`src/discover*.mjs` / `src/dpapi.mjs`）、DPoP + PKCE 自助 OAuth（`src/oauth.mjs`）、refresh_token 一次性轮换（`src/refresh.mjs`）；本项目只取协议与凭据链路，其**批量注册/接码**、Web 控制台、Windows 计划任务看门狗未采用 |
| [luawei1/cline2api](https://github.com/luawei1/cline2api) | Cline 渠道协议逆向参考（MIT；虽未采用其 WorkOS OAuth 方案，模型/对话协议知识有参考，`recommended-models` free 池机制亦借鉴自其分析） |
| [donghangxunlang-cmd/dsh-attention-health](https://github.com/donghangxunlang-cmd/dsh-attention-health) | 思考链死循环（发癫）检测算法来源——`lib/guard.js` 的四条复读/空转判据与防误杀护栏（本项目只取死循环检测，健康评估/工作交接/会话日志未采用） |
| [ttales430/wild-work PR #46](https://github.com/rockswang/wild-work/pull/46) | 智谱清言渠道实现（含 CDP 登录编排） |

> 若上述项目作者认为本项目的引用方式不当，请提 issue 联系，我们会立即调整或移除相关内容。

同时感谢给本项目 PR 的各位贡献者！

以下项目用于**思考强度（reasoning effort）**相关实现的前期调研与比对。本项目只借鉴其公开结论与算法语义，未复制代码：

| 项目 | 贡献 |
|------|------|
| [Lutiancheng1/lingma-proxy](https://github.com/Lutiancheng1/lingma-proxy) | `thinking.budget_tokens` → 档位分桶的换算思路（已采纳） |
| [Sliverkiss/cpa-plugin](https://github.com/Sliverkiss/cpa-plugin) | Qoder 官方请求体模板（`baseprompt.json`），用于确证 body 骨架 |
| [metaphorics/oh-my-pi-plugin-qoder](https://github.com/metaphorics/oh-my-pi-plugin-qoder) | Qoder api3 档位双字段下发的完整实现（MIT） |
| [ly-ccx/Peer-Agent](https://github.com/ly-ccx/Peer-Agent) | Qoder `is_reasoning` 与档位联动、默认档策略（MIT） |
| [kaitranntt/CLIProxyAPIPlus](https://github.com/kaitranntt/CLIProxyAPIPlus) | Go 版 Qoder 模型目录解析与 `parameters` 对象范式（MIT） |
| [bzym2/QoderGateway](https://github.com/bzym2/QoderGateway) | 新版 api2-v2 协议文档（MIT） |
| [apmantza/pi-free](https://github.com/apmantza/pi-free) | 输出侧 thinking 标签的兜底解析思路（MIT） |
| [simonsmh/pi-provider-qoder](https://github.com/simonsmh/pi-provider-qoder) | `thinking_config.enabled.efforts` 类型定义与 per-model 静态档位表 |
| [yuan240324/workbuddy-openai-proxy](https://github.com/yuan240324/workbuddy-openai-proxy) | 请求体纯透传反证上游契约；空回答放大 `max_tokens` 重试思路（MIT） |
| [Wang-JQ77/dsh-trae-api](https://github.com/Wang-JQ77/dsh-trae-api) | Trae Work CN 反代；`reasoning_content` → `<think>` 标签状态机（MIT） |
| [DASungta/trae-proxy](https://github.com/DASungta/trae-proxy) | Trae 系反代；确认**无请求侧思考控制**，反向佐证本项目「TraeWork 不投影」 |
| [arch3rPro/Trae-Proxy](https://github.com/arch3rPro/Trae-Proxy) | 同上，raw 透传（MIT） |
| [OpenSenseNova/SenseNova6.7](https://github.com/OpenSenseNova/SenseNova6.7) | 纯文档仓；响应侧 `reasoning` 字段语义（无请求侧档位） |
| [avaritiachaos/qoder-proxy](https://github.com/avaritiachaos/qoder-proxy) | Qoder 模型表；`qwen3.8-max-effort-{low,medium,high,max}` 证实档位别名只在 Max 系官方暴露（MIT） |
| [alhza/GPT-Load](https://github.com/alhza/GPT-Load) | key 池轮询 / 阈值拉黑 / 递归换 key 重试设计（MIT） |

以下项目用于**同类项目形态**调研（渠道覆盖、模型目录、账号池组织方式）。本项目借鉴其思路与结论，未复制代码：

| 项目 | 贡献 |
|------|------|
| [HUIdada1/AgentHub](https://github.com/HUIdada1/AgentHub) | 动态模型目录（上游拉取 → 落盘缓存 → 别名表并集）、模型元数据与规则文件热加载（MIT） |
| [1416277987/proxy-hub](https://github.com/1416277987/proxy-hub) | 多平台反代统一网关；渠道适配器与模型常量表结构对比 |
| [ttales430/glm2api](https://github.com/ttales430/glm2api) | 智谱清言（chatglm.cn）网页版私有接口协议逆向，glm 渠道协议依据 |
| [ttales430/wild-work PR #46](https://github.com/rockswang/wild-work/pull/46) | 智谱清言渠道实现（含 CDP 登录编排） |

> 若上述项目作者认为本项目的引用方式不当，请提 issue 联系，我们会立即调整或移除相关内容。

同时感谢给本项目 PR 的各位贡献者！

## 使用方式

### Windows

1. 构建或获取 `wild-view.exe`
2. 放到任意目录，双击启动（首次会在同级生成 `wild-view-data/`）
3. 右下角出现托盘图标，**双击**打开 Web 管理面板
4. 在面板中点击渠道按钮添加账号
5. 切换到「模型列表和倍率」查看各渠道目录与倍率

### 托盘菜单

- **打开主界面**：在浏览器中打开管理面板
- **查看日志**：打开 `wild-view-data/data/app.log`
- **退出**：退出程序

### 无头模式（Linux 服务器）

```bash
./wild-view --no-tray
```

启动后打印面板地址，阻塞运行，Ctrl+C 退出。

## 面板操作指南

- **添加账号**：点击渠道按钮 → 确认对话框 → 浏览器完成登录；带「从客户端导入」的渠道可读取本机已登录客户端的凭据。
- **账号管理**：卡片显示积分/Token、额度单位、签到状态；图标按钮为签到 ✓ / 刷新 ↻ / 删除 ✕。
  点击账号名可直接修改显示名。
- **积分明细**：鼠标悬停积分数字查看套餐明细（有效期、可用/不可用小计），条目多时用底部 `‹ ›` 翻页。
- **刷新积分**：面板顶部按钮，批量刷新全部账号余额；单账号失败不影响其他账号。
- **模型列表和倍率**：按渠道分页展示，每个渠道上方标出各账号的查询状态（已同步 / 待刷新 / 失败）。
  同一模型在不同账号倍率不同时会逐账号列出；上游未返回定价显示 `unknown`，只有显式 0 才显示 `Free`。
- **余额变化**：用量页上半部分是每次查询成功时的实际余额曲线，下半部分是积分流水（入项/消耗/到期）。
  两者口径不同：曲线是绝对值，下降可能来自消耗、到期或套餐回收。
- **设置**：顶栏齿轮，包含监听地址、管理密码、纯签到开关与签到时间、临期阈值、开机自启。

### 面板登录（监听非 127.0.0.1 时）

监听非环回地址时必须设置管理密码，否则任何人可打开面板。面板会话使用 HttpOnly cookie，改密码后旧会话立即失效。

## 常见问题

### 1. Windows 提示“未知发布者”或报毒？

未签名的 Go 单文件程序常见误报，可核对发布页的 SHA256 后放行。

### 2. 为什么看不到 API 地址和 API-Key？

本程序不提供会话转发，`/v1/*` 一律 404，因此没有 API 地址与 API-Key 配置。

### 3. 积分显示为 0 或待刷新？

- 首次启动尚未完成查询：点「刷新积分」。
- 该渠道额度接口不支持或需要重新登录：卡片上会显示失败原因。
- 渠道本身没有积分概念（如 Cline、匿名通道）：显示「不适用」。

### 4. 为什么签到按钮是灰的？

纯签到默认关闭，需在设置中开启；另外依赖对话才能完成的签到（WorkBuddy 国际版、智谱清言）不提供入口。

### 5. 可以和 wild-work 同时运行吗？

可以，但必须使用各自的数据目录，且不要并发管理同一份复制凭据（refresh token 会互相轮换失效）。

## ⚠️ 风险警告与免责声明

**在使用本工具前，请务必阅读并理解以下内容。下载、运行或继续使用本工具即表示你已阅读、理解并同意承担全部风险与责任。**

### 风险警告

1. **逆向工程风险**：本工具基于对上游平台（WorkBuddy/CodeBuddy、TraeWork、Qoder、千问办公、OpenCode 等）**未公开接口的逆向工程**实现，非官方支持、无任何授权。上游随时可能调整接口、加密方式或风控策略，导致本工具**部分或全部功能立即失效**，作者不承诺修复时效。
2. **封号风险**：使用本工具的请求特征与官方客户端存在差异，**可能违反上游平台的服务条款**，存在账号被**限制功能、降低额度、暂时或永久封禁**的风险。请自行评估后果，**强烈建议使用小号或可承受损失的账号接入，切勿将主力账号用于本工具**。
3. **网络暴露风险**：面板监听 `0.0.0.0`（或任意非 `127.0.0.1` 地址）时**必须配置管理密码**（`config.json` 的 `admin_password`，
   或面板设置中「管理密码」），否则程序拒绝启动；未配置密码时只能监听本机。即便如此，仍请确认网络环境可信。
4. **数据安全风险**：账号凭证（access/refresh token）以明文 JSON 保存在本地 `auths/` 目录，请妥善保管该目录，避免泄露。

### 免责声明

1. 本工具仅供**个人学习、技术研究与测试**用途，严禁用于商业用途、大规模滥用或任何违反法律法规及上游平台服务条款的场景。
2. 本软件按"**现状**"（AS IS）提供，**不提供任何明示或默示的保证**。作者不对使用本软件造成的任何直接或间接损失承担责任，包括但不限于：账号被封禁、积分/额度损失、数据丢失、商业纠纷、法律纠纷等。
3. 使用者应**自行承担**因使用本工具产生的一切风险与后果；若不同意上述条款，请立即停止使用并删除本软件。
4. 本工具与上游各平台及其运营方**无任何关联与合作**；相关商标与品牌权利归其各自所有者所有。
5. 如本工具的使用对上游平台造成影响或平台方提出异议，作者将配合处理；本项目的存在不代表对任何规避行为的价值判断。

## License

[MIT](LICENSE) © rockswang。

欢迎 fork、修改与二次分发（含以独立仓库形式发布的衍生作品），只需按 MIT 条款保留版权声明与许可声明。
仅供个人学习研究使用，请遵守各上游平台服务条款。
