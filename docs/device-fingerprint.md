# 设备指纹（fingerPrint）计算与使用说明

> 面向：想搞清楚「学校凭什么判断这台设备可不可信」的同学，以及本程序（seuszjz）的维护者。
> 结论全部来自抓取 `auth.seu.edu.cn` 前端 bundle（`umi.5d913cb9.js`，内置 FingerprintJS **4.6.2**）与实测响应；属于推断的部分会单独标注。

---

## 0. 一句话结论

**指纹不是讲座平台（ehall）算的，而是统一身份认证（`auth.seu.edu.cn`）的登录页在浏览器里算出来的**：

```
fingerPrint = "stable_" + x64hash128( 16 项稳定设备特征的序列化串 , 种子 0 )   // 共 39 个字符
```

登录时它随 `POST /auth/casback/casLogin` 一起提交；服务端据此判断「这台设备是否已信任」，不信任就返回 `502 非可信设备，需要二次验证`，要求短信验证码；勾选「记住此设备」并通过验证后，服务端把 **这个字符串** 记为可信设备。

---

## 1. 名词

| 名词 | 含义 |
| --- | --- |
| CAS / 统一身份认证 | `auth.seu.edu.cn`，全校账号登录入口 |
| fingerPrint / 设备指纹 | 浏览器端算出的设备哈希，格式 `stable_` + 32 位十六进制 |
| 可信设备 | 服务端登记过、再次登录不需要二次验证的设备（按 fingerPrint 记录） |
| stage2 / 二次验证 | 短信验证码那一步；接口返回 `502` 即触发 |
| `rememberTrustDevice` | 弹窗里「记住此设备，下次免二次验证」勾选框的值 |

---

## 2. 指纹是怎么算出来的（逐层拆解）

### 2.1 采集：FingerprintJS `get()`

登录页组件挂载时（`componentWillMount`）异步执行：

```js
const fp = await FingerprintJS.load();      // 版本 4.6.2
const result = await fp.get();              // { visitorId, components, confidence, version }
```

`components` 里是**几十项**浏览器特征（字体、Canvas、WebGL、音频、屏幕、时区、语言……），其中一部分会随每次刷新变化（例如 `documentLoop`）。所以平台**没有直接用 `visitorId`**，而是只取「稳定子集」自己再哈希一遍。

### 2.2 只取稳定子集（16 项）

```js
STABLE_COMPONENT_KEYS = [
  "fonts", "platform", "architecture", "hardwareConcurrency",
  "screenResolution", "touchSupport", "cookiesEnabled", "pdfViewerEnabled",
  "canvas", "timezone", "dateTimeLocale", "colorGamut",
  "math", "audioBaseLatency", "webGlBasics", "languages"
];
```

遍历上面的列表，`components[key]` 存在才收进来（`undefined` 跳过），组成一个新的小对象。

| 组件 | 采集内容 | 典型影响因素 |
| --- | --- | --- |
| `fonts` | 系统字体探测结果 | 装/卸载字体、系统字体更新 |
| `platform` | `navigator.platform` | 操作系统 |
| `architecture` | CPU 架构 | 换机器、虚拟机 |
| `hardwareConcurrency` | 逻辑核心数 | 换机、虚拟机核数 |
| `screenResolution` | 屏幕分辨率 | 显示器、系统缩放 |
| `touchSupport` | 最大触点数、`ontouchstart` | 是否有触摸屏 |
| `cookiesEnabled` | Cookie 是否可用 | 隐私设置 |
| `pdfViewerEnabled` | 内置 PDF 阅读器 | 浏览器设置 |
| `canvas` | Canvas 绘制的像素哈希 | 显卡驱动、字体、抗锯齿 |
| `timezone` | 时区（IANA 名） | 系统时区 |
| `dateTimeLocale` | `Intl.DateTimeFormat` 结果 | 系统语言/区域 |
| `colorGamut` | 支持的色域（sRGB/P3/rec2020） | 显示器 |
| `math` | 数学函数特征值 | CPU / JS 引擎 |
| `audioBaseLatency` | `AudioContext.baseLatency` | 声卡 / 驱动 |
| `webGlBasics` | WebGL 厂商、渲染器、版本、扩展 | 显卡驱动 |
| `languages` | `navigator.languages` | 浏览器语言设置 |

> 这解释了为什么「换浏览器 / 换机器 / 改语言 / 改缩放 / 换显卡驱动」都会让指纹变化。

### 2.3 序列化：把对象压成一个字符串

bundle 里的真实实现（反混淆后语义等价）：

```js
function componentsToString(components) {
  let out = "";
  for (const key of Object.keys(components).sort()) {          // ① key 按字典序排序
    const comp = components[key];
    const value = ("error" in comp) ? "error" : JSON.stringify(comp.value);   // ② 出错统一记 "error"
    out += (out ? "|" : "") + key.replace(/([:|\\])/g, "\\$1") + ":" + value; // ③ key:value，用 | 连接
  }
  return out;
}
```

规则明确：

1. key 排序 → 保证顺序稳定；
2. 值统一 `JSON.stringify(component.value)`；
3. `key:value` 用 `|` 拼接，key 里的 `:`、`|`、`\` 会被反斜杠转义。

### 2.4 哈希：murmur3 x64，输出 32 位十六进制

```js
function hashComponents(components) { return x64hash128(componentsToString(components)); }
```

`x64hash128(str, seed = 0)` 是 FingerprintJS 内置的 murmur3 x64 变体，bundle 里能直接看到它的常量：

| 常量 | 值 |
| --- | --- |
| 块混合 c1 | `0x87c37b91 0x114253d5`（十进制 `2277735313, 289559509`） |
| 块混合 c2 | `0x4cf5ad43 0x2745937f`（十进制 `1291169091, 658871167`） |
| 初始 h | `0x52dce729` / `0x38495ab5`（十进制 `1390208809, 944331445`） |
| 终混 fmix64 | `0xff51afd7 0xed558ccd` / `0xc4ceb9fe 0x1a85ec53` |

输出拼接方式（决定了长度）：

```js
("00000000" + (h1 >>> 0).toString(16)).slice(-8) +   // 8 位
("00000000" + (h2 >>> 0).toString(16)).slice(-8) +   // 8 位
("00000000" + (h3 >>> 0).toString(16)).slice(-8) +   // 8 位
("00000000" + (h4 >>> 0).toString(16)).slice(-8)     // 8 位 → 共 32 位小写十六进制
```

### 2.5 加前缀

```js
deviceFingerprint = "stable_" + hash;   // 例：stable_a4f1358cba0ab170a5e661513cddf352
this.setState({ deviceFingerprint });   // 存进组件 state，登录时取出
```

长度固定 **39**：`stable_`（7 个字符）+ 32 位十六进制。

### 2.6 三级降级链

平台自己也怕算不出来，所以准备了兜底：

| 顺序 | 触发条件 | 结果 | 形态 |
| --- | --- | --- | --- |
| 1 | 正常 | `"stable_" + hashComponents(稳定子集)` | `stable_` + 32 hex |
| 2 | 采集/哈希抛异常 | 官方 `visitorId`（全部组件的哈希） | 32 hex（**没有** `stable_` 前缀） |
| 3 | 连库都加载失败 | `generateFallbackFingerprint()` | `fb_` + 8 hex |

第 3 级的实现（比较粗糙，只用了 7 个原始字段）：

```js
function generateFallbackFingerprint() {
  const raw = JSON.stringify({
    userAgent: navigator.userAgent, platform: navigator.platform, language: navigator.language,
    screenWidth: screen.width, screenHeight: screen.height,
    colorDepth: screen.colorDepth, timezoneOffset: new Date().getTimezoneOffset()
  });
  let h = 0;
  for (let i = 0; i < raw.length; i++) { h = (h << 5) - h + raw.charCodeAt(i); h &= h; }  // djb2 风格
  return "fb_" + Math.abs(h).toString(16).padStart(8, "0");
}
```

> 实测看到的都是第 1 级形态（`stable_` 开头）；第 2、3 级只在库加载失败时出现。

---

## 3. 它在登录协议里的位置

### 3.1 完整时序

```
浏览器                                       auth.seu.edu.cn
  │  GET /dist/                                  │
  ├─────────────────────────────────────────────►│  载入 umi bundle
  │  组件挂载 → FingerprintJS 采集 → stable_xxx   │
  │  POST /auth/casback/verifyTgt {service}      │
  ├─────────────────────────────────────────────►│
  │  GET  /auth/casback/needCaptcha              │
  ├─────────────────────────────────────────────►│  {needStage2Validation:false}
  │  POST /auth/casback/getChiperKey {}          │
  ├─────────────────────────────────────────────►│  {publicKey:"MIGf…"}
  │  POST /auth/casback/casLogin                 │
  │  { service, username, password: RSA(密码),     │
  │    captcha:"", rememberMe, loginType:"account",
  │    wxBinded:false, mobilePhoneNum:"",        │
  │    mobileVerifyCode:null,                    │
  │    fingerPrint:"stable_xxx",                 │
  │    rememberTrustDevice:false }               │
  ├─────────────────────────────────────────────►│
  │◄──── 200 {code:502, info:"非可信设备，需要二次验证"} ─── 弹出二次验证框
  │  用户勾选「记住此设备」                         │
  │  POST /auth/casback/sendStage2Code {userId}  │
  ├─────────────────────────────────────────────►│
  │◄──── {code:200, info:"验证码已发送 1xx…，2分钟有效"}
  │  用户填验证码 → RSA 加密                       │
  │  POST /auth/casback/casLogin（同一个会话 Cookie）│
  │  { …, mobileVerifyCode: RSA(验证码),          │
  │       rememberTrustDevice: true }            │
  ├─────────────────────────────────────────────►│
  │◄──── {code:200, success:true, redirectUrl:"…"}                  │
  │  window.location.href = redirectUrl           │
```

### 3.2 `casLogin` 请求体（字段全貌）

```json
{
  "service": "https://vpn.seu.edu.cn:443/passport/v1/auth/cas?sfDomain=CAS-auth",
  "username": "220265496",
  "password": "<RSA 加密后的 base64>",
  "captcha": "",
  "rememberMe": true,
  "loginType": "account",
  "wxcode": "",
  "wxBinded": false,
  "agentId": "",
  "mobilePhoneNum": "",
  "mobileVerifyCode": "<RSA 加密后的验证码 base64，第一次为 null>",
  "fingerPrint": "stable_a4f1358cba0ab170a5e661513cddf352",
  "rememberTrustDevice": false
}
```

- `password` 与 `mobileVerifyCode` 都用 `getChiperKey` 返回的公钥做 **RSA PKCS#1 v1.5 + base64**；
- `fingerPrint` 就是第 2 节算出来的字符串；
- `rememberTrustDevice` 只在二次验证那一次为 `true`（等价于勾选「记住此设备」）。

### 3.3 实测响应（原文）

首次登录被判为陌生设备：

```json
{ "code": 502, "info": "非可信设备，需要二次验证", "success": false, "needStage2Validation": false }
```

发码（注意 5004 是误导性文案，见 §5.3）：

```json
{ "code": 5004, "info": "用户未登记手机号", "success": false }
{ "code": 200,  "info": "验证码已发送 186****5315，2分钟有效", "success": true }
```

验证码错误：

```json
{ "code": 502, "info": "验证码错误", "success": false }
```

---

## 4. 服务端如何使用这个值（推断）

从行为反推（**这一节是推断，非官方文档**）：

1. 每个账号维护一份可信设备清单，键是 `fingerPrint` 字符串；
2. `casLogin` 时比对当前 `fingerPrint`：
   - 命中 → 直接放行，不再要短信；
   - 未命中 → 返回 `502`，走短信二次验证；
3. 二次验证成功且 `rememberTrustDevice: true` → 把 **本次请求携带的 fingerPrint** 写入可信清单，下次命中。

因此「记住此设备」能否生效，取决于**后续登录是否携带同一个 fingerPrint**。

---

## 5. 为什么本程序会「每次都要短信验证」

### 5.1 我们发的指纹和浏览器不是同一个值

| | 值 | 是否设备特征 |
| --- | --- | --- |
| 浏览器 | `stable_` + x64hash128(16 项设备特征) | ✅ 真正的设备画像 |
| 本程序（默认） | `stable_` + md5(学号) | ❌ 只是账号的固定串 |

两者**格式合法、长度一致、对同一账号长期稳定**，所以服务端不会报错，但它认为「这是一台从没见过的设备」——于是每次都要求二次验证。

### 5.2 还有别的差异会干扰判定

- **User-Agent**：本程序默认是 `Mozilla/5.0 (X11; Linux x86_64) … Chrome/148`，而普通浏览器是 Windows Chrome；
- **缺少浏览器侧信号**：`sec-ch-ua`、`Accept-Language`、Canvas/字体等根本不存在于纯 HTTP 客户端；
- 若服务端把信任与「指纹 + UA / 出口 IP」共同绑定，上面任一不匹配都可能让信任失效。

### 5.3 「用户未登记手机号」往往是假的

同一账号连续发码实测：

```
第 1 次（刚发过）: {"code":5004,"info":"用户未登记手机号","success":false}
第 2 次（20 秒后）: {"code":200,"info":"验证码已发送 186****5315，2分钟有效","success":true}
```

服务端第二次自己把手机号打出来了，说明**手机号是登记过的**，`5004` 实际是「发码太频繁 / 暂时不可用」的兜底文案。
本程序因此在 `SendStage2Code` 里做了 `0s / 3s / 15s` 三次重试。

---

## 6. 本程序当前的做法（以及试过但已放弃的方案）

### 6.1 当前实现：`stable_` + md5(学号)

`internal/seu/login.go` 里发的是：

```go
"fingerPrint": "stable_" + hex(md5(用户名)),
```

即同一个账号永远是同一个值，格式与平台一致（`stable_` + 32 位十六进制），对服务端而言相当于「一台固定但不认识的设备」。

### 6.2 试过：在 WebView 里自动算真实设备指纹（已移除）

Wails 的界面跑在 WebView2 里，理论上可以内嵌同版本 FingerprintJS、用同一套 16 项特征 + `hashComponents` 算出「真实设备指纹」。实测结论：

- 自动生成成功，格式与平台一致；
- 但**没有解决二次验证**：即使带着 `rememberTrustDevice: true` 验证通过，重新登录仍会要求短信；
- 说明服务端对「可信设备」的判定还依赖其它信号（UA、出口 IP、或浏览器侧特征），不是单靠这个字符串；
- 加上「让用户去 DevTools 抄指纹」在校园网外根本不可行，于是整套功能被移除（`frontend/src/device.ts`、`@fingerprintjs/fingerprintjs` 依赖、`SessionService.SetDeviceFingerprint` 等）。

所以现在程序**不再**自动生成、也不提供手动填写指纹入口。

### 6.3 二次验证目前怎么处理

程序识别到 `502 非可信设备，需要二次验证` 后会：

1. 调 `POST /auth/casback/sendStage2Code {"userId": 学号}` 发短信（内部 0s/3s/15s 重试，躲避「5004」这个误导性报错）；
2. 用户在登录页输入验证码，程序用同一把 RSA 公钥加密后带 `mobileVerifyCode` + `rememberTrustDevice: true` 重提 `casLogin`；
3. 成功后照常走 aTrust / ehall 登录，并缓存会话 Cookie（重启程序靠 Cookie 复用，尽量不重复走密码登录）。
## 7. 排错清单

| 现象 | 可能原因 | 处理 |
| --- | --- | --- |
| 验证通过后仍反复要短信 | 信任绑定不止指纹（UA/IP 等），自动方案已放弃 | 接受每次都验证，或改用网页端 |
| 浏览器换设备/重装 | 指纹变了，旧信任失效 | 重新取一次指纹 |
| 发码报 `5004 用户未登记手机号` | 多为发码太频繁 | 等 20–30 秒；程序已内置 3 次重试 |
| 验证码一直说错误 | 超过 2 分钟有效期，或不是同一会话发的码 | 重新发码再填；本程序保证「发码-提交」走同一会话 |
| 每次登录都要短信 | 服务端不认为这台设备可信（UA/IP/指纹任一不匹配） | 见 §6.2；属于已知限制，程序会照常引导走短信 |
| 抢座过程中被踢下线 | 手机 App / 浏览器又登录了同一账号（双设备限制） | 抢座期间不要在其他设备打开讲座页 |

---

## 8. 附录：证据出处

- 登录页 bundle：`https://auth.seu.edu.cn/dist/umi.<hash>.js`（约 1.3 MB，内含 FingerprintJS 4.6.2）
- 关键片段（反混淆后语义等价）：

```js
// 生成指纹
m = Ao.hashComponents(f);            // f = 16 项稳定子集
g = "stable_" + m;                   // 前缀
this.setState({ deviceFingerprint: g });

// 提交登录
payload: { …, fingerPrint: state.deviceFingerprint,
              rememberTrustDevice: state.stage2RememberDevice }

// 502 => 弹出二次验证框
else if (502 == e.code) setState({ stage2ModalVisible: true, stage2ValidateCode: null });

// 发码
POST /auth/casback/sendStage2Code  {"userId": <学号>}

// 弹窗文案
「本次登录需要二次验证」/「记住此设备，下次免二次验证」/「短信验证码」/「获取验证码」
```

- 本程序对应实现：
  - `internal/seu/login.go`：`casLogin`（带 `fingerPrint` / `mobileVerifyCode` / `rememberTrustDevice`）、`SendStage2Code`（含重试与手机号脱敏）、`LoginWithStage2`、`deviceFingerprint()`
  - `internal/seu/session.go`：`Config.Fingerprint`、`ErrStage2Required`
  - `service.go` / `service_types.go`：`Settings.DeviceFingerprint`
  - `frontend/src/components/views/SettingsView.vue`：设备指纹输入框

---

## 9. 安全与隐私

- 指纹是**设备特征的哈希**，不含账号、密码、手机号；但它能**唯一标识一台设备**，属于可关联标识符，不要随手发到公开渠道。
- 短信验证码是一次性凭证：本程序只用于当次登录，提交后立即丢弃，不写日志、不落盘（发码成功文案里的手机号也会脱敏成 `186****5315`）。
- 早期版本尝试过「复用浏览器指纹」的思路，因未能真正解决二次验证且普通用户无法取得该值，已移除；现在只保留标准的短信二次验证流程。
