package seu

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// probeTimeout 是登录前网络探测的超时。探测只是一次匿名 GET, 正常几十毫秒,
// 留 6 秒是为了容忍网络抖动, 同时不至于让用户久等。
const probeTimeout = 6 * time.Second

// directTimeout 是「直连 ehall 试一次」的时间预算。自动探测判成校园网、实际
// 直连却走不通时, 不能让人干等 appLogin 内部的多轮重试, 所以这条路单独限时。
const directTimeout = 15 * time.Second

// stageFunc 接收登录进度回调 (stage 用于分组, msg 为人类可读描述)。
type stageFunc func(stage, msg string)

func (s *Session) progress(fn stageFunc, stage, msg string) {
	if fn != nil {
		fn(stage, msg)
	}
}

// Login 执行完整登录流程。
//
// 校外: CAS -> aTrust 网关 -> ehall; 校园网: CAS -> ehall (跳过网关)。
// 走哪条路由 Config.NetMode 决定, 默认 auto: 先匿名请求一次 ehall 首页, 看它
// 会不会被网关 302 到 vpn.seu.edu.cn, 据此判断要不要过网关。
// 两条路互为兜底, 但只有「网络不可达」才换路; 密码错误、票据为空之类的业务
// 失败直接报错, 不做无谓重试。
func (s *Session) Login(ctx context.Context, progress func(stage, msg string)) error {
	return s.login(ctx, "", progress)
}

// LoginWithStage2 带二次验证码重新完成登录。
//
// 学校对陌生设备返回 502「非可信设备」时, 前端先调 SendStage2Code 发短信, 用户输入后
// 再调这里; 内部会用同一个会话(同一份 Cookie)重新提交 casLogin, 并带上 rememberTrustDevice,
// 让学校把这台设备记成可信设备。
func (s *Session) LoginWithStage2(ctx context.Context, code string, progress func(stage, msg string)) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return &LoginError{Msg: "请填写短信验证码"}
	}
	return s.login(ctx, code, progress)
}

// login 是 Login / LoginWithStage2 的公共实现; stage2Code 为空表示没有二次验证码。
func (s *Session) login(ctx context.Context, stage2Code string, progress func(stage, msg string)) error {
	s.Reset()
	if strings.TrimSpace(s.cfg.Username) == "" {
		return &LoginError{Msg: "请填写学号"}
	}
	if s.cfg.Password == "" {
		return &LoginError{Msg: "请填写密码"}
	}
	if err := s.casLogin(ctx, progress, stage2Code); err != nil {
		return err
	}

	var err error
	if s.resolveNetMode(ctx) == NetCampus {
		err = s.loginEhallDirect(ctx, progress)
		if err != nil {
			// 少数校内节点仍然要求先过网关, 按老路再试一次。
			s.progress(progress, "portal", "直连 ehall 未成功, 改用 aTrust 网关重试")
			s.setNetMode(NetOffCampus)
			if perr := s.portalLogin(ctx, progress); perr != nil {
				return err
			}
			err = s.loginEhall(ctx, progress)
		}
	} else {
		err = s.portalLogin(ctx, progress)
		if err == nil {
			err = s.loginEhall(ctx, progress)
		} else if IsUnreachable(err) {
			// 网关根本连不上 (校园网、或 DNS/路由异常): 直接换直连。
			s.progress(progress, "portal", "aTrust 网关不可达, 改为直连 ehall")
			s.setNetMode(NetCampus)
			if derr := s.loginEhallDirect(ctx, progress); derr == nil {
				err = nil
			} else {
				s.setNetMode(NetOffCampus)
			}
		}
	}
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.loggedIn = true
	s.mu.Unlock()
	s.progress(progress, "done", "登录成功")
	return nil
}

// loginEhall 换取 ehall 会话, 直连与过网关两条路共用。
//
// 直连时拿不到网关的 onlineInfo, 用户信息退化为学号; 界面照常显示,
// 不能因为缺个昵称就判登录失败。
func (s *Session) loginEhall(ctx context.Context, progress func(stage, msg string)) error {
	if err := s.appLogin(ctx, progress); err != nil {
		return err
	}
	s.mu.Lock()
	if s.user.Username == "" {
		name := s.cfg.Username
		s.user = UserInfo{Username: name, DisplayName: name, Online: true}
	}
	s.mu.Unlock()
	return nil
}

// loginEhallDirect 是带时间预算的直连尝试: 超时或失败由调用方决定是否换路。
func (s *Session) loginEhallDirect(ctx context.Context, progress func(stage, msg string)) error {
	directCtx, cancel := context.WithTimeout(ctx, directTimeout)
	defer cancel()
	return s.loginEhall(directCtx, progress)
}

// resolveNetMode 决定这次登录走直连还是 aTrust 网关。
func (s *Session) resolveNetMode(ctx context.Context) NetMode {
	mode := ParseNetMode(string(s.cfg.NetMode))
	if mode != NetAuto {
		s.setNetMode(mode)
		return mode
	}
	mode = s.probeNetMode(ctx)
	s.setNetMode(mode)
	s.logf("网络探测结果: %s", mode)
	return mode
}

// probeNetMode 用一次匿名的 ehall 请求判断要不要过 aTrust 网关。
//
// 校外访问 ehall 会被网关截住, 302 到 vpn.seu.edu.cn/controller/v1/public/verify;
// 校园网内不会有这一跳 (直接给应用页, 或者跳统一身份认证)。这个信号比「vpn 域名
// 通不通」可靠得多: ehall 和 vpn 在公网都解析到公网 IP、443 都能连, 拨号探测
// 区分不了两者。
func (s *Session) probeNetMode(ctx context.Context) NetMode {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	resp, err := s.do(probeCtx, request{method: "GET", target: EhallRoot})
	if err != nil {
		// 探测失败 (断网/DNS 异常) 时按校外处理: 校外是默认场景, 且网关路径
		// 在校园网里连不上时还会回落到直连。
		s.logf("网络探测失败 (%v), 按校外处理", err)
		return NetOffCampus
	}
	location := strings.ToLower(resp.header.Get("Location"))
	if strings.Contains(location, "vpn.seu.edu.cn") {
		return NetOffCampus
	}
	return NetCampus
}

// IsUnreachable 判断错误是否属于「这条路根本不通」(DNS 失败、拨号超时、
// 连接被拒), 用来决定换一条网络路径, 而不是直接把错误抛给用户。
func IsUnreachable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, hint := range []string{
		"i/o timeout",
		"no such host",
		"connection refused",
		"connection reset",
		"network is unreachable",
		"tls handshake timeout",
		"connectex",
	} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

// stageError 包装带原始原因的错误, 让上层能分辨网络问题还是业务问题。
func stageError(stage string, err error) error {
	return &LoginError{Stage: stage, Msg: err.Error(), Cause: err}
}

// casLogin: 统一身份认证, 用 RSA 加密后的密码换取 CAS 会话。
func (s *Session) casLogin(ctx context.Context, progress func(stage, msg string), stage2Code string) error {
	stage := "cas"
	s.progress(progress, stage, "正在验证统一身份认证账号")

	headers := map[string]string{
		"Origin":  AuthBase,
		"Referer": AuthBase + "/dist/",
	}

	if _, err := s.do(ctx, request{method: "GET", target: AuthBase + "/dist/"}); err != nil {
		return &LoginError{Stage: "统一身份认证", Msg: "无法访问认证服务器, 请检查网络: " + err.Error()}
	}

	var tgt map[string]any
	_, err := s.doJSON(ctx, request{
		method:  "POST",
		target:  AuthBase + "/auth/casback/verifyTgt",
		json:    map[string]any{"service": CASService},
		headers: headers,
	}, &tgt)
	if err != nil {
		return &LoginError{Stage: "统一身份认证", Msg: err.Error()}
	}

	var captcha map[string]any
	if _, err := s.doJSON(ctx, request{
		method:  "GET",
		target:  AuthBase + "/auth/casback/needCaptcha",
		headers: headers,
	}, &captcha); err != nil {
		return &LoginError{Stage: "统一身份认证", Msg: err.Error()}
	}
	if truthy(captcha["needStage2Validation"]) && stage2Code == "" {
		// 前置检查就要二次验证: 让上层去发验证码。
		s.progress(progress, stage, "需要二次验证, 请先发送短信验证码")
		return &LoginError{Stage: "统一身份认证", Msg: "需要二次验证", Cause: ErrStage2Required}
	}
	if code, ok := captcha["code"]; ok && rawString(code) != "200" && rawString(code) != "" {
		return &LoginError{Stage: "统一身份认证", Msg: "认证前置检查失败: " + rawString(captcha["info"])}
	}

	var keyInfo map[string]any
	if _, err := s.doJSON(ctx, request{
		method:  "POST",
		target:  AuthBase + "/auth/casback/getChiperKey",
		json:    map[string]any{},
		headers: headers,
	}, &keyInfo); err != nil {
		return &LoginError{Stage: "统一身份认证", Msg: err.Error()}
	}
	publicKey := rawString(keyInfo["publicKey"])
	if publicKey == "" {
		return &LoginError{Stage: "统一身份认证", Msg: "获取加密公钥失败"}
	}

	encrypted, err := encryptPassword(publicKey, s.cfg.Password)
	if err != nil {
		return &LoginError{Stage: "统一身份认证", Msg: err.Error()}
	}
	sum := md5.Sum([]byte(s.cfg.Username))

	// 二次验证码同样要用同一把 RSA 公钥加密后再提交 (与浏览器一致)。
	var verifyCode any
	rememberTrust := false
	if stage2Code != "" {
		encryptedCode, err := encryptPassword(publicKey, stage2Code)
		if err != nil {
			return &LoginError{Stage: "统一身份认证", Msg: err.Error()}
		}
		verifyCode = encryptedCode
		// 勾上「信任此设备」: 学校会记住下面的设备指纹, 之后不再要求二次验证。
		rememberTrust = true
	}

	payload := map[string]any{
		"service":             CASService,
		"username":            s.cfg.Username,
		"password":            encrypted,
		"captcha":             "",
		"rememberMe":          false,
		"loginType":           "account",
		"wxBinded":            false,
		"mobilePhoneNum":      "",
		"mobileVerifyCode":    verifyCode,
		"fingerPrint":         "stable_" + hex.EncodeToString(sum[:]),
		"rememberTrustDevice": rememberTrust,
	}

	var result map[string]any
	if _, err := s.doJSON(ctx, request{
		method:  "POST",
		target:  AuthBase + "/auth/casback/casLogin",
		json:    payload,
		headers: headers,
	}, &result); err != nil {
		return &LoginError{Stage: "统一身份认证", Msg: err.Error()}
	}
	// 502 + 「非可信设备」= 学校要求二次验证; 其它非 success 都是业务失败。
	if rawString(result["code"]) == "502" || strings.Contains(rawString(result["info"]), "非可信设备") {
		if stage2Code == "" {
			s.progress(progress, stage, "当前设备需要二次验证, 请先发送短信验证码")
			return &LoginError{Stage: "统一身份认证", Msg: "需要二次验证", Cause: ErrStage2Required}
		}
		return &LoginError{Msg: "二次验证未通过: " + rawString(result["info"])}
	}
	if !truthy(result["success"]) {
		msg := rawString(result["info"])
		if msg == "" {
			msg = "账号或密码错误"
		}
		return &LoginError{Msg: "登录失败: " + msg}
	}
	if truthy(result["needStage2Validation"]) && stage2Code == "" {
		s.progress(progress, stage, "需要二次验证, 请先发送短信验证码")
		return &LoginError{Stage: "统一身份认证", Msg: "需要二次验证", Cause: ErrStage2Required}
	}
	s.progress(progress, stage, "统一身份认证通过")
	return nil
}

// SendStage2Code 给当前账号绑定的手机发一条二次验证短信。
//
// 接口: POST /auth/casback/sendStage2Code {"userId": 学号} (与浏览器前端一致)。
func (s *Session) SendStage2Code(ctx context.Context) error {
	var out map[string]any
	_, err := s.doJSON(ctx, request{
		method: "POST",
		target: AuthBase + "/auth/casback/sendStage2Code",
		json:   map[string]any{"userId": s.cfg.Username},
		headers: map[string]string{
			"Origin":  AuthBase,
			"Referer": AuthBase + "/dist/",
		},
	}, &out)
	if err != nil {
		return &LoginError{Stage: "二次验证", Msg: "发送验证码失败: " + err.Error()}
	}
	if rawString(out["code"]) != "200" {
		msg := rawString(out["info"])
		if msg == "" {
			msg = "发送验证码失败"
		}
		return &LoginError{Stage: "二次验证", Msg: msg}
	}
	return nil
}

// portalLogin: aTrust 网关换票 + 上报设备 + 确认在线。
func (s *Session) portalLogin(ctx context.Context, progress func(stage, msg string)) error {
	stage := "portal"
	s.progress(progress, stage, "正在建立 aTrust 网关会话")

	// 网关接口不接受 CAS 的 Origin 头。
	var ticketInfo map[string]any
	if _, err := s.doJSON(ctx, request{
		method: "POST",
		target: AuthBase + "/auth/casback/verifyTgt",
		json:   map[string]any{"service": CASService},
		headers: map[string]string{
			"Referer": AuthBase + "/dist/",
		},
	}, &ticketInfo); err != nil {
		return stageError("aTrust 网关", err)
	}
	redirect := rawString(ticketInfo["redirectUrl"])
	if redirect == "" {
		return &LoginError{Stage: "aTrust 网关", Msg: "CAS 未下发网关票据: " + rawString(ticketInfo["info"])}
	}

	resp, err := s.do(ctx, request{method: "GET", target: redirect})
	if err != nil {
		return stageError("aTrust 网关", err)
	}
	location := resp.header.Get("Location")
	if location == "" {
		return &LoginError{Stage: "aTrust 网关", Msg: fmt.Sprintf("网关票据兑换失败 (HTTP %d)", resp.status)}
	}

	portalURL, err := url.Parse(location)
	if err != nil {
		return &LoginError{Stage: "aTrust 网关", Msg: "网关返回的门户地址无法解析"}
	}
	var portalData map[string]any
	if err := json.Unmarshal([]byte(portalURL.Query().Get("data")), &portalData); err != nil {
		return &LoginError{Stage: "aTrust 网关", Msg: "网关返回的门户参数无法解析"}
	}
	ticket := rawString(portalData["ticket"])
	if ticket == "" {
		return &LoginError{Stage: "aTrust 网关", Msg: "网关票据为空"}
	}

	referer := resolveURL(PortalBase, location)
	s.portalReferer = referer
	if _, err := s.do(ctx, request{method: "GET", target: referer}); err != nil {
		return stageError("aTrust 网关", err)
	}

	deviceID := newDeviceID()
	envHeaders := map[string]string{
		"x-csrf-token":  s.csrf,
		"x-sdp-traceid": randomHex(4),
		"Referer":       referer,
	}
	if _, err := s.do(ctx, request{
		method:  "POST",
		target:  PortalBase + "/controller/v1/public/reportEnv",
		query:   portalParams(),
		headers: envHeaders,
		json: map[string]any{
			"ticket":   ticket,
			"deviceId": deviceID,
			"env": map[string]any{
				"endpoint": map[string]any{
					"device_id": deviceID,
					"device":    map[string]any{"type": "browser"},
				},
			},
		},
	}); err != nil {
		return stageError("aTrust 网关", err)
	}

	var check map[string]any
	if _, err := s.doJSON(ctx, request{
		method:  "GET",
		target:  PortalBase + "/passport/v1/auth/authCheck",
		query:   portalParams(),
		headers: envHeaders,
	}, &check); err != nil {
		return stageError("aTrust 网关", err)
	}
	if rawString(check["code"]) != "0" {
		return &LoginError{Stage: "aTrust 网关", Msg: "网关在线校验失败: " + rawString(check["message"])}
	}

	onlineInfo, _ := check["data"].(map[string]any)
	info, _ := onlineInfo["onlineInfo"].(map[string]any)
	s.mu.Lock()
	s.userRaw = info
	s.user = mapUserInfo(info, s.cfg.Username)
	s.mu.Unlock()

	name := s.user.DisplayName
	if name == "" {
		name = s.user.Username
	}
	s.progress(progress, stage, "网关已放行: "+name)
	return nil
}

// appLogin: 过网关访问 ehall, 再用应用层 CAS 票据换取 ehall 会话。
//
// ehall 换票链路偶发某一跳长时间无响应 (网关侧), 因此这里做多次重试,
// 每跳使用较短超时, 一旦拿到 GS_SESSIONID 立即停止。
func (s *Session) appLogin(ctx context.Context, progress func(stage, msg string)) error {
	stage := "ehall"
	s.progress(progress, stage, "正在登录 ehall 应用")

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			s.gatewayWarmup(ctx)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		redirect, err := s.appTicket(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		if err := s.followAppTicket(ctx, redirect); err != nil {
			lastErr = err
			continue
		}
		if s.hasCookie("GS_SESSIONID") {
			s.progress(progress, stage, "已获得 ehall 会话")
			return nil
		}
		lastErr = &LoginError{Stage: "ehall", Msg: "没有拿到 ehall 会话 Cookie, 当前: " + strings.Join(s.jar.Names(), ", ")}
	}
	if lastErr == nil {
		lastErr = &LoginError{Stage: "ehall", Msg: "ehall 登录失败"}
	}
	return lastErr
}

// appTicket 申请应用层 CAS 票据。
func (s *Session) appTicket(ctx context.Context) (string, error) {
	var ticketInfo map[string]any
	if _, err := s.doJSON(ctx, request{
		method: "POST",
		target: AuthBase + "/auth/casback/verifyTgt",
		json:   map[string]any{"service": AppService},
		headers: map[string]string{
			"Referer": AuthBase + "/dist/",
		},
	}, &ticketInfo); err != nil {
		return "", &LoginError{Stage: "ehall", Msg: err.Error()}
	}
	redirect := rawString(ticketInfo["redirectUrl"])
	if redirect == "" {
		return "", &LoginError{Stage: "ehall", Msg: "应用票据申请失败: " + rawString(ticketInfo["info"])}
	}
	return redirect, nil
}

// followAppTicket 逐跳跟随后端重定向完成换票。
// 拿到会话后 ehall 会继续重定向到一个会挂起的地址, 所以拿到 Cookie 就停。
func (s *Session) followAppTicket(ctx context.Context, redirect string) error {
	current := redirect
	for hop := 0; hop < 8; hop++ {
		hopCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		resp, err := s.do(hopCtx, request{method: "GET", target: current})
		cancel()
		if err != nil {
			return err
		}
		if s.hasCookie("GS_SESSIONID") {
			return nil
		}
		if !resp.isRedirect() {
			return nil
		}
		next := resolveURL(resp.url.String(), resp.header.Get("Location"))
		if next == "" || next == current {
			return nil
		}
		current = next
	}
	return nil
}

// gatewayWarmup 预访问 ehall 首页, 跟随少量网关跳转 (忽略失败)。
func (s *Session) gatewayWarmup(ctx context.Context) {
	resp, err := s.do(ctx, request{method: "GET", target: AppURL})
	if err != nil {
		return
	}
	for i := 0; i < 6; i++ {
		if !resp.isRedirect() {
			return
		}
		location := resolveURL(resp.url.String(), resp.header.Get("Location"))
		if location == "" || strings.Contains(location, "auth.seu.edu.cn") {
			return
		}
		hopCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		resp, err = s.do(hopCtx, request{method: "GET", target: location})
		cancel()
		if err != nil {
			return
		}
	}
}

func (s *Session) hasCookie(name string) bool {
	for _, n := range s.jar.Names() {
		if n == name {
			return true
		}
	}
	return false
}

// resolveURL 等价于 Python 的 urljoin。
func resolveURL(base, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	refURL, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	if refURL.IsAbs() {
		return refURL.String()
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return ref
	}
	return baseURL.ResolveReference(refURL).String()
}

// truthy 处理 JSON 里可能出现的 true/"true"/1/"1"。
func truthy(v any) bool {
	switch rawString(v) {
	case "1", "true", "TRUE", "True", "yes":
		return true
	}
	return false
}

// mapUserInfo 把网关返回的 onlineInfo 映射成固定结构。
func mapUserInfo(raw map[string]any, fallback string) UserInfo {
	info := UserInfo{
		Username:    rawString(raw["username"]),
		DisplayName: rawString(raw["displayName"]),
		ClientIP:    rawString(raw["clientIp"]),
		LoginTime:   rawString(raw["loginTime"]),
		Online:      truthy(raw["isOnline"]),
	}
	if info.Username == "" {
		info.Username = fallback
	}
	if info.DisplayName == "" {
		info.DisplayName = info.Username
	}
	return info
}
