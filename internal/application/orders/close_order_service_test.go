package orders

import (
	"context"
	"testing"
)

// closeRuntimeFake 在手动发货运行时替身上加上卖家关闭订单能力。
type closeRuntimeFake struct {
	manualShipRuntimeFake
	// closing 是平台关闭结果。
	closing PriceAdjustment
	// calls 记录平台关闭请求的订单。
	calls []string
}

// CloseOrderBySeller 返回测试平台关闭结果。
func (f *closeRuntimeFake) CloseOrderBySeller(_ context.Context, _, orderID string, _ int64) PriceAdjustment {
	f.calls = append(f.calls, orderID)
	return f.closing
}

// TestCloseOrderOnlyForUnpaidOrWaitingOrders 验证只有待付款、待发货订单请求平台关闭，成功后本地记为 cancelled。
func TestCloseOrderOnlyForUnpaidOrWaitingOrders(t *testing.T) {
	repository := &manualShipRepositoryFake{ownedIDs: []string{"cookie-1"}, orders: map[string]*Order{
		"unpaid":  {OrderID: "unpaid", CookieID: "cookie-1", OrderStatus: "1"},
		"paid":    {OrderID: "paid", CookieID: "cookie-1", OrderStatus: "2"},
		"shipped": {OrderID: "shipped", CookieID: "cookie-1", OrderStatus: "3"},
		"other":   {OrderID: "other", CookieID: "cookie-9", OrderStatus: "1"},
	}}
	runtime := &closeRuntimeFake{manualShipRuntimeFake: manualShipRuntimeFake{mtopAvailable: true}, closing: PriceAdjustment{Success: true}}
	service := NewManualShipService(repository, runtime)
	for orderID, status := range map[string]string{"unpaid": "succeeded", "paid": "succeeded", "shipped": "not_closable", "other": "forbidden", "missing": "not_found"} {
		result, err := service.CloseOrder(context.Background(), CloseOrderRequest{UserID: 7, OrderID: orderID})
		if err != nil || result.Status != status {
			t.Fatalf("%s 应为 %s: result=%+v err=%v", orderID, status, result, err)
		}
	}
	if len(runtime.calls) != 2 || repository.upsertOptions.OrderStatus != "cancelled" {
		t.Fatalf("只有待付款、待发货订单请求平台，成功后记 cancelled: calls=%v upsert=%+v", runtime.calls, repository.upsertOptions)
	}
	runtime.closing = PriceAdjustment{Uncertain: true}
	if result, _ := service.CloseOrder(context.Background(), CloseOrderRequest{UserID: 7, OrderID: "unpaid"}); result.Status != "uncertain" {
		t.Fatalf("结果未知应为 uncertain: %+v", result)
	}
}
