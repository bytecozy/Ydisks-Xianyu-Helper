package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	orderapp "xianyu-go/internal/application/orders"
)

// orderPricePortFake 在订单端口替身上加上待付款订单改价。
type orderPricePortFake struct {
	orderAdapterPortFake
	// result 是改价用例结果。
	result orderapp.AdjustPriceResult
	// last 是最近一次改价请求。
	last *orderapp.AdjustPriceRequest
}

// AdjustOrderPrice 返回测试配置的改价结果。
func (f orderPricePortFake) AdjustOrderPrice(_ context.Context, request orderapp.AdjustPriceRequest) (orderapp.AdjustPriceResult, error) {
	*f.last = request
	return f.result, nil
}

// TestAdjustOrderPriceRoute 验证改价接口的成功契约和错误约定：409 未付款以外、502 *_failed / *_uncertain、503 未装配。
func TestAdjustOrderPriceRoute(t *testing.T) {
	server, _, cleanup := newTestServer(t)
	defer cleanup()
	handler := server.Router()
	session := loginHelper(t, handler)
	var last orderapp.AdjustPriceRequest
	server.applications.orders = orderPricePortFake{result: orderapp.AdjustPriceResult{OrderID: "o1", Status: "succeeded", Message: "闲鱼已确认改价"}, last: &last}

	recorder := serveOpenAPISuccess(t, handler, session, http.MethodPost, "/api/v1/orders/o1/adjust-price", `{"price_cents":14900}`)
	if !strings.Contains(recorder.Body.String(), `"price_cents":14900`) || last.OrderID != "o1" || last.PriceCents != 14900 {
		t.Fatalf("成功响应异常: %s %+v", recorder.Body.String(), last)
	}

	cases := []struct {
		status     string
		body       string
		wantStatus int
		wantCode   string
	}{
		{"not_pending_payment", `{"price_cents":14900}`, 409, "order_not_pending_payment"},
		{"failed", `{"price_cents":14900}`, 502, "order_price_failed"},
		{"uncertain", `{"price_cents":14900}`, 502, "order_price_uncertain"},
		{"not_found", `{"price_cents":14900}`, 404, ""},
		{"succeeded", `{"price_cents":0}`, 400, ""},
	}
	for _, tc := range cases {
		server.applications.orders = orderPricePortFake{result: orderapp.AdjustPriceResult{OrderID: "o1", Status: tc.status, Message: "原因"}, last: &last}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/orders/o1/adjust-price", strings.NewReader(tc.body))
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(session)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		var payload struct{ Code string }
		_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
		if recorder.Code != tc.wantStatus || (tc.wantCode != "" && payload.Code != tc.wantCode) {
			t.Fatalf("%s: status=%d body=%s", tc.status, recorder.Code, recorder.Body.String())
		}
		assertOpenAPIResponse(t, request, recorder)
	}

	// 订单端口没有改价能力（旧装配）时返回 503，不请求平台。
	server.applications.orders = orderAdapterPortFake{}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/orders/o1/adjust-price", strings.NewReader(`{"price_cents":14900}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(session)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("未装配应为 503: %d %s", recorder.Code, recorder.Body.String())
	}
}
