package orders

import (
	"context"
	"errors"
	"testing"
)

// priceRuntimeFake 在手动发货运行时替身上加上待付款订单改价能力。
type priceRuntimeFake struct {
	manualShipRuntimeFake
	// adjustment 是平台改价结果。
	adjustment PriceAdjustment
	// calls 记录平台改价请求次数和金额。
	calls []int64
}

// AdjustOrderPrice 返回测试平台改价结果。
func (f *priceRuntimeFake) AdjustOrderPrice(_ context.Context, _, _ string, priceCents int64, _ int64) PriceAdjustment {
	f.calls = append(f.calls, priceCents)
	return f.adjustment
}

// TestAdjustPriceSucceedsOnlyForPendingPaymentOrders 验证只有待付款订单请求平台改价，成功后写回金额。
func TestAdjustPriceSucceedsOnlyForPendingPaymentOrders(t *testing.T) {
	repository := &manualShipRepositoryFake{ownedIDs: []string{"cookie-1"}, orders: map[string]*Order{
		"unpaid": {OrderID: "unpaid", CookieID: "cookie-1", OrderStatus: "1"},
		"paid":   {OrderID: "paid", CookieID: "cookie-1", OrderStatus: "2"},
		"other":  {OrderID: "other", CookieID: "cookie-9", OrderStatus: "1"},
	}}
	runtime := &priceRuntimeFake{manualShipRuntimeFake: manualShipRuntimeFake{mtopAvailable: true}, adjustment: PriceAdjustment{Success: true}}
	service := NewManualShipService(repository, runtime)

	result, err := service.AdjustPrice(context.Background(), AdjustPriceRequest{UserID: 7, OrderID: "unpaid", PriceCents: 14900})
	if err != nil || result.Status != "succeeded" || repository.upsertOptions.Amount != "149.00" {
		t.Fatalf("改价结果异常: result=%+v err=%v upsert=%+v", result, err, repository.upsertOptions)
	}
	for orderID, status := range map[string]string{"paid": "not_pending_payment", "other": "forbidden", "missing": "not_found"} {
		result, err = service.AdjustPrice(context.Background(), AdjustPriceRequest{UserID: 7, OrderID: orderID, PriceCents: 14900})
		if err != nil || result.Status != status {
			t.Fatalf("%s 应为 %s: result=%+v err=%v", orderID, status, result, err)
		}
	}
	if len(runtime.calls) != 1 {
		t.Fatalf("只有待付款订单应请求平台: %v", runtime.calls)
	}
}

// TestAdjustPriceTellsFailedFromUncertain 验证平台明确拒绝与结果未知分开返回，且都不重试。
func TestAdjustPriceTellsFailedFromUncertain(t *testing.T) {
	repository := &manualShipRepositoryFake{ownedIDs: []string{"cookie-1"}, orders: map[string]*Order{"unpaid": {OrderID: "unpaid", CookieID: "cookie-1", OrderStatus: "1"}}}
	runtime := &priceRuntimeFake{manualShipRuntimeFake: manualShipRuntimeFake{mtopAvailable: true}, adjustment: PriceAdjustment{Messages: []string{"FAIL_BIZ::订单已关闭"}}}
	service := NewManualShipService(repository, runtime)
	if result, _ := service.AdjustPrice(context.Background(), AdjustPriceRequest{UserID: 7, OrderID: "unpaid", PriceCents: 100}); result.Status != "failed" {
		t.Fatalf("平台拒绝应为 failed: %+v", result)
	}
	runtime.adjustment = PriceAdjustment{Uncertain: true, Err: errors.New("connection reset")}
	if result, _ := service.AdjustPrice(context.Background(), AdjustPriceRequest{UserID: 7, OrderID: "unpaid", PriceCents: 100}); result.Status != "uncertain" {
		t.Fatalf("结果未知应为 uncertain: %+v", result)
	}
	if len(runtime.calls) != 2 || repository.upsertID != "" {
		t.Fatalf("每次只请求一次平台、未成功不写本地: calls=%v upsert=%q", runtime.calls, repository.upsertID)
	}
}

// TestAdjustPriceUnavailableWithoutAdjuster 验证运行时未实现改价能力时返回不可用。
func TestAdjustPriceUnavailableWithoutAdjuster(t *testing.T) {
	service := NewManualShipService(&manualShipRepositoryFake{}, &manualShipRuntimeFake{mtopAvailable: true})
	if _, err := service.AdjustPrice(context.Background(), AdjustPriceRequest{UserID: 7, OrderID: "x", PriceCents: 100}); !errors.Is(err, ErrPriceAdjustUnavailable) {
		t.Fatalf("应返回 ErrPriceAdjustUnavailable: %v", err)
	}
}
