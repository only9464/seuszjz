//go:build !js

package seu

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	// dialAttemptTimeout 是单次拨号的超时。因为多个地址是同时竞速的,
	// 这里可以给得宽一点: 只要有一个地址通, 几十毫秒就返回了。
	dialAttemptTimeout = 8 * time.Second
	// dialStagger 是地址之间的错开间隔: 第一个先拨, 300ms 没结果再拨第二个,
	// 避免每次都同时打四个 IP 造成无谓的连接。
	dialStagger = 300 * time.Millisecond
	// dnsTimeout 是单次解析超时, dnsRetry 是解析失败后的重试次数。
	dnsTimeout = 5 * time.Second
	dnsRetry   = 3
	// dnsRetryDelay 是解析重试的初始间隔 (每次翻倍)。
	dnsRetryDelay = 150 * time.Millisecond
	// addrCacheTTL 是解析结果的缓存时长; dnsNegativeTTL 是解析失败后的冷却。
	addrCacheTTL   = 10 * time.Minute
	dnsNegativeTTL = 3 * time.Second
	// badIPCooldown 是「连不通的地址」的冷却时长, 冷却期内排到最后再试。
	badIPCooldown = 60 * time.Second
)

// dialer 是给 HTTP Transport 用的拨号器, 解决两个校园网里真实存在的问题:
//
//  1. 一个域名解析出多个地址, 其中一部分连不通 (SEU 的 ehall 就有 202.119.24.x
//     被网络挡住, 只有 222.190.112.125 能连)。net.Dialer 默认按顺序一个个试,
//     撞到被挡的地址要等满拨号超时才轮到下一个, 表现就是登录一直转圈或直接失败。
//  2. 校园网 DNS 偶发 NXDOMAIN (瞬时抖动), 单次解析失败就判死刑太脆。
//
// 所以这里做三件事: 解析重试 + 结果缓存; 多地址错开竞速, 谁先通用谁;
// 记住连不通的地址, 冷却期内排到最后。
type dialer struct {
	resolver *net.Resolver
	logf     func(format string, args ...any)

	mu    sync.Mutex
	addrs map[string]addrEntry
	bad   map[string]time.Time
	neg   map[string]time.Time
}

type addrEntry struct {
	ips     []string
	expires time.Time
}

// fallbackHosts 是「域名解析不出来、但地址本身稳定且可连」时的兜底表。
//
// 实测踩到的问题: ehall 会 302 到
// https://vpn.seu.edu.cn:443/controller/v1/public/verify?t=…, 而某些网络的 DNS
// 里根本没有 vpn.seu.edu.cn 这条记录 (校园网/隧道内 DNS 实测都这样), 于是整条
// aTrust 流程在第一跳就断了。网关与 ehall 的地址是固定的, 这里按域名兜底,
// 只在 DNS 解析失败时启用, 解析正常时完全不参与。
var fallbackHosts = map[string][]string{
	// 多个候选一起交给竞速拨号, 哪个通用哪个 (202.119.24.x 在部分网络被挡,
	// 所以第一个才是最常用的那个)。
	"vpn.seu.edu.cn":   {"121.249.14.2", "202.119.24.12", "202.119.24.18"},
	"ehall.seu.edu.cn": {"222.190.112.125", "202.119.24.12", "202.119.24.18"},
}

// dialOutcome 是竞速拨号里单个地址的结果。
type dialOutcome struct {
	conn net.Conn
	err  error
	ip   string
}

func newDialer(logf func(format string, args ...any)) *dialer {
	return &dialer{
		resolver: net.DefaultResolver,
		logf:     logf,
		addrs:    make(map[string]addrEntry),
		bad:      make(map[string]time.Time),
		neg:      make(map[string]time.Time),
	}
}

func (d *dialer) debugf(format string, args ...any) {
	if d.logf != nil {
		d.logf(format, args...)
	}
}

// DialContext 实现 http.Transport 的拨号签名。host 是域名 (Transport 把串行
// 解析留给我们), 这里自己解析、自己挑地址。
func (d *dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	// 已经是 IP (或被 Transport 直接给了地址) 就不用解析。
	if net.ParseIP(host) != nil {
		return d.dialOne(ctx, network, addr)
	}

	ips, err := d.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	ordered := d.preferHealthy(ips)
	if len(ordered) == 1 {
		return d.dialOne(ctx, network, net.JoinHostPort(ordered[0], port))
	}

	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel() // 未命中的 goroutine 靠它提前退出; 成功路径里的显式 cancel 也保留
	results := make(chan dialOutcome, len(ordered))
	for i, ip := range ordered {
		go func(index int, ip string) {
			if index > 0 {
				select {
				case <-time.After(time.Duration(index) * dialStagger):
				case <-raceCtx.Done():
					results <- dialOutcome{err: raceCtx.Err(), ip: ip}
					return
				}
			}
			conn, err := d.dialOne(raceCtx, network, net.JoinHostPort(ip, port))
			results <- dialOutcome{conn: conn, err: err, ip: ip}
		}(i, ip)
	}

	var lastErr error
	for i := 0; i < len(ordered); i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case out := <-results:
			if out.err == nil {
				d.markGood(out.ip)
				d.debugf("连接 %s (%s) 成功", host, out.ip)
				cancel()
				go drainConns(results, len(ordered)-i-1)
				return out.conn, nil
			}
			if !errors.Is(out.err, context.Canceled) {
				d.markBad(out.ip)
				d.debugf("连接 %s (%s) 失败: %v", host, out.ip, out.err)
			}
			lastErr = out.err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("连接 %s 失败", host)
	}
	return nil, lastErr
}

// drainConns 回收竞速里迟到的连接, 避免泄漏。
func drainConns(results <-chan dialOutcome, n int) {
	for i := 0; i < n; i++ {
		if out, ok := <-results; ok && out.conn != nil {
			_ = out.conn.Close()
		}
	}
}

func (d *dialer) dialOne(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: dialAttemptTimeout}
	return dialer.DialContext(ctx, network, addr)
}

// lookup 解析域名: 命中缓存直接返回, 失败按 150/300/450ms 重试三次。
//
// 校园网 DNS 会偶发 NXDOMAIN, 重试能把这种瞬时失败吃掉; 真正的「域名不存在」
// (比如校园网里的 vpn.seu.edu.cn) 会在三次都失败后返回错误, 只是多花不到 1 秒。
func (d *dialer) lookup(ctx context.Context, host string) ([]string, error) {
	now := time.Now()
	d.mu.Lock()
	if entry, ok := d.addrs[host]; ok && now.Before(entry.expires) {
		ips := append([]string(nil), entry.ips...)
		d.mu.Unlock()
		return ips, nil
	}
	if until, ok := d.neg[host]; ok && now.Before(until) {
		d.mu.Unlock()
		return nil, fmt.Errorf("解析 %s 失败 (冷却中)", host)
	}
	d.mu.Unlock()

	var lastErr error
	for attempt := 0; attempt < dnsRetry; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * dnsRetryDelay):
			}
		}
		lookupCtx, cancel := context.WithTimeout(ctx, dnsTimeout)
		ips, err := d.resolver.LookupHost(lookupCtx, host)
		cancel()
		if err == nil && len(ips) > 0 {
			d.mu.Lock()
			d.addrs[host] = addrEntry{ips: ips, expires: time.Now().Add(addrCacheTTL)}
			d.mu.Unlock()
			d.debugf("解析 %s -> %v", host, ips)
			return ips, nil
		}
		lastErr = err
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ctx.Err()
		}
	}

	// DNS 彻底失败: 如果这个域名有内置兜底地址, 用它 (SNI/Host 仍然是原域名)。
	if pinned := fallbackHosts[host]; len(pinned) > 0 {
		ips := append([]string(nil), pinned...)
		d.mu.Lock()
		d.addrs[host] = addrEntry{ips: ips, expires: time.Now().Add(addrCacheTTL)}
		d.mu.Unlock()
		d.debugf("解析 %s 失败, 改用内置兜底地址 %v", host, ips)
		return ips, nil
	}

	d.mu.Lock()
	d.neg[host] = time.Now().Add(dnsNegativeTTL)
	d.mu.Unlock()
	if lastErr == nil {
		lastErr = errors.New("没有解析结果")
	}
	return nil, fmt.Errorf("解析 %s 失败: %w", host, lastErr)
}

// preferHealthy 把冷却期内的坏地址排到最后, 其余保持解析顺序。
func (d *dialer) preferHealthy(ips []string) []string {
	now := time.Now()
	good := make([]string, 0, len(ips))
	bad := make([]string, 0, len(ips))
	d.mu.Lock()
	for _, ip := range ips {
		if until, ok := d.bad[ip]; ok && now.Before(until) {
			bad = append(bad, ip)
			continue
		}
		good = append(good, ip)
	}
	d.mu.Unlock()
	return append(good, bad...)
}

func (d *dialer) markBad(ip string) {
	d.mu.Lock()
	d.bad[ip] = time.Now().Add(badIPCooldown)
	d.mu.Unlock()
}

func (d *dialer) markGood(ip string) {
	d.mu.Lock()
	delete(d.bad, ip)
	d.mu.Unlock()
}
