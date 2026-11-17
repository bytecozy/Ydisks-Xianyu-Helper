package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	orderapp "xianyu-go/internal/application/orders"
)

// orderClosePortFake 在订单端口替身上加上卖家关闭订单。
type orderClosePortFake struct {
	orderAdapterPortFake
	// result 是关闭订单用例结果。
	result orderapp.AdjustPriceResult
}

// CloseOrder 返回测试配置的关闭结果。
func (f orderClosePortFake) CloseOrder(context.Context, orderapp.CloseOrderRequest) (orderapp.AdjustPriceResult, error) {
	return f.result, nil
}

// TestCloseOrderRoute 验证关闭订单接口的成功契约和错误约定。
func TestCloseOrderRoute(t *testing.T) {
	server, _, cleanup := newTestServer(t)
	defer cleanup()
	handler := server.Router()
	session := loginHelper(t, handler)
	server.applications.orders = orderClosePortFake{result: orderapp.AdjustPriceResult{OrderID: "o1", Status: "succeeded", Message: "闲鱼已关闭订单"}}
	serveOpenAPISuccess(t, handler, session, http.MethodPost, "/api/v1/orders/o1/close", "")

	cases := []struct {
		status     string
		wantStatus int
		wantCode   string
	}{
		{"not_closable", 409, "order_not_closable"},
		{"failed", 502, "order_close_failed"},
		{"uncertain", 502, "order_close_uncertain"},
		{"not_found", 404, ""},
	}
	for _, tc := range cases {
		server.applications.orders = orderClosePortFake{result: orderapp.AdjustPriceResult{OrderID: "o1", Status: tc.status, Message: "原因"}}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/orders/o1/close", nil)
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
	server.applications.orders = orderAdapterPortFake{}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/orders/o1/close", nil)
	request.AddCookie(session)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("未装配应为 503: %d", recorder.Code)
	}
}
