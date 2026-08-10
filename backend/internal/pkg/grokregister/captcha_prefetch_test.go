package grokregister

import (
	"testing"
	"time"
)

// CaptchaPrefetch 核心行为:
//  1. Start 幂等(同 worker 同代理不重复发起)
//  2. Take 代理匹配 → 返回结果通道;不匹配/无预取 → nil
//  3. 结果可复用注册流程的消费方式(chan 读取)

func TestPrefetchTakeProxyMatch(t *testing.T) {
	p := NewCaptchaPrefetch()
	p.Start(0, "socks5://a:1", CaptchaOptions{Provider: CaptchaProviderNone}, nil)
	ch := p.Take(0, "socks5://a:1")
	if ch == nil {
		t.Fatal("代理匹配时应返回结果通道")
	}
	// 第二次 Take(已取走)应为 nil
	if ch2 := p.Take(0, "socks5://a:1"); ch2 != nil {
		t.Fatal("已取走的预取不应再返回")
	}
}

func TestPrefetchTakeProxyMismatch(t *testing.T) {
	p := NewCaptchaPrefetch()
	p.Start(1, "socks5://a:1", CaptchaOptions{Provider: CaptchaProviderNone}, nil)
	if ch := p.Take(1, "socks5://b:2"); ch != nil {
		t.Fatal("代理不匹配时应返回 nil(调用方走正常 solve 兜底)")
	}
	// 原槽仍在,正确代理还能取
	if ch := p.Take(1, "socks5://a:1"); ch == nil {
		t.Fatal("代理匹配的预取应仍可取")
	}
}

func TestPrefetchStartIdempotent(t *testing.T) {
	p := NewCaptchaPrefetch()
	p.Start(2, "socks5://a:1", CaptchaOptions{Provider: CaptchaProviderNone}, nil)
	p.Start(2, "socks5://a:1", CaptchaOptions{Provider: CaptchaProviderNone}, nil) // 幂等:不重复
	ch := p.Take(2, "socks5://a:1")
	if ch == nil {
		t.Fatal("同代理重复 Start 不应破坏预取")
	}
}

func TestPrefetchClear(t *testing.T) {
	p := NewCaptchaPrefetch()
	p.Start(3, "socks5://a:1", CaptchaOptions{Provider: CaptchaProviderNone}, nil)
	p.Clear(3)
	if ch := p.Take(3, "socks5://a:1"); ch != nil {
		t.Fatal("Clear 后不应再有预取")
	}
}

// 真实 solve 路径:预取 goroutine 应能完成并把结果写入通道(用 127.0.0.1 无效代理,
// 验证的是通道语义而不是 solve 成败 —— err 结果同样要能消费)。
func TestPrefetchChannelDelivers(t *testing.T) {
	p := NewCaptchaPrefetch()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// 用一个必然快速失败的目标,验证结果通道会收到(不论成败)
		p.Start(4, "socks5://127.0.0.1:1", CaptchaOptions{
			Provider:      CaptchaProviderAuralith,
			AuralithURL:   "http://127.0.0.1:1",
			SiteURL:       "https://accounts.x.ai/sign-up",
			SiteKey:       "test",
			ProxyURL:      "socks5://127.0.0.1:1",
			ProxyExplicit: true,
		}, nil)
		ch := p.Take(4, "socks5://127.0.0.1:1")
		if ch == nil {
			t.Error("预取通道应为非 nil")
			return
		}
		select {
		case <-ch:
			// 有结果(成功或错误)即通过 —— 通道语义正确
		case <-time.After(15 * time.Second):
			t.Error("预取结果 15s 内未送达")
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("测试超时")
	}
}
