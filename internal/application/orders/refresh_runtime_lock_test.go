package orders

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// runtimeLockProbe 模拟生产运行时 Cookie 同步再次获取同一账号凭证锁，避免用死锁超时作为测试断言。
type runtimeLockProbe struct {
	// refreshRuntimeFake 提供合成平台详情和 Cookie 写回结果。
	refreshRuntimeFake
	// credentials 是与订单刷新仓储共享的账号凭证锁。
	credentials *sync.Mutex
	// called 记录响应发生 Cookie 轮换后确实同步了运行实例。
	called bool
	// held 记录回调被调用时凭证锁仍然占用，此时真实生产回调会自锁。
	held bool
}

// UpdateRunningCookie 以非阻塞探针复现生产回调取得同一凭证锁的调用边界；ctx、cookieID、value 不涉及真实凭证。
func (r *runtimeLockProbe) UpdateRunningCookie(ctx context.Context, cookieID, value string) {
	r.called = true
	if !r.credentials.TryLock() {
		r.held = true
		return
	}
	r.credentials.Unlock()
}

// TestRefreshSingleCookieRotationNotifiesRuntimeOutsideCredentialLock 覆盖正常 Cookie 轮换、Token 错误伴随轮换和旧扁平 Cookie 更新。
func TestRefreshSingleCookieRotationNotifiesRuntimeOutsideCredentialLock(t *testing.T) {
	// scenario 枚举持久化 Cookie 后需要通知运行时的三种真实分支。
	for _, scenario := range []string{"jar-success", "jar-token-error", "flat-success"} {
		// t 管理单个 Cookie 写回分支的生命周期与断言。
		t.Run(scenario, func(t *testing.T) {
			// repository 在详情前读取和响应写回阶段使用同一把账号锁。
			repository := &chatLockRepository{refreshRepositoryFake: &refreshRepositoryFake{owned: map[string]bool{"cookie-1": true}, order: &Order{OrderID: "order-1", CookieID: "cookie-1", OrderStatus: "processing"}, detail: &PlatformRuntimeData{UserID: 7, Value: "fixture"}}}
			// runtime 提供 Cookie 轮换结果，并探测同步回调是否仍在锁内。
			runtime := &runtimeLockProbe{credentials: &repository.mu, refreshRuntimeFake: refreshRuntimeFake{detailAvailable: true, persistConfigured: true, persistValue: "rotated-fixture", persistChanged: true, persistHandled: true, detailResult: RefreshDetailFetchResult{Detail: &RefreshDetail{OrderStatus: "pending_ship", UpdatedCookies: "rotated-fixture"}}}}
			if scenario == "jar-token-error" {
				runtime.detailErr = errors.New("FAIL_SYS_TOKEN_EMPTY::令牌为空")
			}
			if scenario == "flat-success" {
				runtime.persistHandled = false
			}
			// err 仅在 Token 错误场景保留平台失败，不影响响应 Cookie 的运行时收口。
			_, err := NewRefreshService(repository, runtime, 1).RefreshSingle(context.Background(), 7, "order-1")
			if runtime.held || !runtime.called {
				t.Fatalf("运行时 Cookie 更新必须在凭证锁外执行: called=%v held=%v", runtime.called, runtime.held)
			}
			if (err != nil) != (scenario == "jar-token-error") {
				t.Fatalf("平台错误未按原样保留: %v", err)
			}
		})
	}
}
