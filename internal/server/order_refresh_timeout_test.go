package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	orderapp "xianyu-go/internal/application/orders"
)

// TestRefreshSingleOrderDeadlineContract 验证总时限传到应用用例，超时返回标准 504 契约而非无界等待。
func TestRefreshSingleOrderDeadlineContract(t *testing.T) {
	// server、cleanup 保存带真实路由契约依赖的本地测试服务器。
	server, _, cleanup := newTestServer(t)
	defer cleanup()
	// server 的订单端口检查调用方预算；ctx 来自 HTTP Handler，userID/orderID 只用于本地隔离请求。
	server.applications.orders = orderAdapterPortFake{refreshSingleFn: func(ctx context.Context, userID int64, orderID string) (orderapp.SingleRefreshResult, error) {
		// deadline、bounded 保存 Handler 设置的截止时刻及是否存在总预算。
		deadline, bounded := ctx.Deadline()
		if !bounded || time.Until(deadline) > singleOrderRefreshTimeout || time.Until(deadline) < singleOrderRefreshTimeout-time.Second {
			t.Fatalf("单订单刷新缺少预期总时限: bounded=%v deadline=%v", bounded, deadline)
		}
		return orderapp.SingleRefreshResult{}, context.DeadlineExceeded
	}}
	// request 带合成会话和订单路径，直接经过生产 Handler 的错误映射。
	request := requestWithServerSession(http.MethodPost, "/api/v1/orders/o1/refresh", map[string]string{"order_id": "o1"})
	// recorder 捕获包含 order_refresh_timeout 错误码的标准响应。
	recorder := httptest.NewRecorder()
	server.refreshSingleOrder(recorder, request)
	assertOpenAPIExpectedStatusResponse(t, request, recorder, http.StatusGatewayTimeout)
}
