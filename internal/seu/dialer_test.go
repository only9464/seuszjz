package seu

import (
	"context"
	"net"
	"testing"
	"time"
)

// seedAddrs 预置一条解析结果, 让测试不依赖真实 DNS。
func seedAddrs(d *dialer, host string, ips ...string) {
	d.mu.Lock()
	d.addrs[host] = addrEntry{ips: ips, expires: time.Now().Add(time.Minute)}
	d.mu.Unlock()
}

// TestDialerRacesIPs 检查多个地址会竞速: 第一个连不通时, 第二个仍能连上。
func TestDialerRacesIPs(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("无法监听本地端口: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	d := newDialer(nil)
	// 127.0.0.2 上没有监听, 连接会被立刻拒绝; 127.0.0.1 是好的那个。
	seedAddrs(d, "race.test", "127.0.0.2", "127.0.0.1")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("race.test", port))
	if err != nil {
		t.Fatalf("应当连上可用的地址, 实际失败: %v", err)
	}
	_ = conn.Close()

	if _, bad := d.bad["127.0.0.2"]; !bad {
		t.Fatal("连不通的地址应被记入冷却名单")
	}
	if _, bad := d.bad["127.0.0.1"]; bad {
		t.Fatal("可用地址不应被记为坏地址")
	}
}

// TestDialerPreferHealthy 检查冷却期内的地址被排到最后。
func TestDialerPreferHealthy(t *testing.T) {
	d := newDialer(nil)
	d.markBad("10.0.0.2")
	got := d.preferHealthy([]string{"10.0.0.2", "10.0.0.1"})
	if len(got) != 2 || got[0] != "10.0.0.1" {
		t.Fatalf("坏地址应排到最后, 实际 %v", got)
	}
}

// TestDialerUsesCachedAddrs 检查命中缓存时不再解析。
func TestDialerUsesCachedAddrs(t *testing.T) {
	d := newDialer(nil)
	seedAddrs(d, "cached.test", "192.0.2.10")
	ips, err := d.lookup(context.Background(), "cached.test")
	if err != nil {
		t.Fatalf("缓存未命中: %v", err)
	}
	if len(ips) != 1 || ips[0] != "192.0.2.10" {
		t.Fatalf("缓存结果不对: %v", ips)
	}
}

// TestDialerLookupFailure 检查解析失败会重试并返回错误 (用 .invalid 顶级域, 必定 NXDOMAIN)。
func TestDialerLookupFailure(t *testing.T) {
	d := newDialer(nil)
	start := time.Now()
	if _, err := d.lookup(context.Background(), "no-such-host.invalid"); err == nil {
		t.Fatal("不存在的域名应返回错误")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("解析失败应当快速返回, 实际耗时 %s", elapsed)
	}
	// 冷却期内第二次调用应该立即失败 (不再走 DNS)。
	start = time.Now()
	if _, err := d.lookup(context.Background(), "no-such-host.invalid"); err == nil {
		t.Fatal("冷却期内也应返回错误")
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("冷却期应立刻返回, 实际耗时 %s", elapsed)
	}
}

// TestDialerDirectIP 检查传进来的已经是 IP 时不做解析。
func TestDialerDirectIP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("无法监听本地端口: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	d := newDialer(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("直接给 IP 应当能连上: %v", err)
	}
	_ = conn.Close()
}
