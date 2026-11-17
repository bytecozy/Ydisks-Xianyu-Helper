package orders

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrPriceAdjustUnavailable 表示订单运行时没有装配待付款订单改价能力。
var ErrPriceAdjustUnavailable = errors.New("订单改价能力未初始化")

// AdjustPriceRequest 描述卖家把一笔待付款订单改成新总价的请求（货源台 goofish 专用，逐单）。
type AdjustPriceRequest struct {
	// UserID 是当前登录用户标识。
	UserID int64
	// OrderID 是待付款订单标识。
	OrderID string
	// PriceCents 是改价后的订单总价，单位为分。
	PriceCents int64
}

// AdjustPriceResult 描述单笔订单改价结果。
// Status 为 succeeded（平台确认已改）、failed（平台明确未改，或改价前就被拒绝）、uncertain（无法判断平台是否已改，不得自动重试）、
// not_pending_payment（本地订单不是待付款，未请求平台）、not_found、forbidden。
type AdjustPriceResult struct {
	// OrderID 是结果对应的订单标识。
	OrderID string
	// Status 是结果状态。
	Status string
	// Message 是面向调用方的结果说明。
	Message string
}

// PriceAdjustment 是平台订单改价调用的结果和凭证变化。
type PriceAdjustment struct {
	// Success 表示平台确认改价成功。
	Success bool
	// Uncertain 表示请求可能已到达平台但结果未知。
	Uncertain bool
	// Messages 是平台返回的业务提示。
	Messages []string
	// RuntimeCookie 是需要同步到运行时的最新 Cookie。
	RuntimeCookie string
	// RuntimeCookieChanged 表示 RuntimeCookie 是否需要同步。
	RuntimeCookieChanged bool
	// Err 是远端调用或会话处理错误。
	Err error
}

// OrderPriceAdjuster 是订单运行时可选实现的待付款订单改价能力。
type OrderPriceAdjuster interface {
	// AdjustOrderPrice 调用平台把待付款订单改成 priceCents 分。
	AdjustOrderPrice(ctx context.Context, cookieID, orderID string, priceCents int64, userID int64) PriceAdjustment
}

// AdjustPrice 把当前用户账号下一笔待付款订单改成新总价；只请求平台一次，结果不确定时不重试。
func (s *ManualShipService) AdjustPrice(ctx context.Context, request AdjustPriceRequest) (AdjustPriceResult, error) {
	if s == nil || s.repository == nil || s.runtime == nil {
		return AdjustPriceResult{}, errors.New("订单改价依赖未初始化")
	}
	// adjuster 是运行时的改价能力；未装配时由 HTTP 层返回 503。
	adjuster, ok := s.runtime.(OrderPriceAdjuster)
	if !ok || !s.runtime.MTopAvailable() {
		return AdjustPriceResult{}, ErrPriceAdjustUnavailable
	}
	orderID := strings.TrimSpace(request.OrderID)
	result := AdjustPriceResult{OrderID: orderID}
	ownedCookieIDs, err := s.repository.ListOwnedIDs(ctx, request.UserID)
	if err != nil {
		return AdjustPriceResult{}, err
	}
	order, getErr := s.repository.GetOrder(ctx, orderID)
	if getErr != nil || order == nil {
		result.Status, result.Message = "not_found", "订单不存在"
		return result, nil
	}
	if !containsOwnedCookieID(ownedCookieIDs, order.CookieID) {
		result.Status, result.Message = "forbidden", "无权操作此订单"
		return result, nil
	}
	if NormalizeOrderStatus(strings.TrimSpace(order.OrderStatus)) != "processing" {
		result.Status, result.Message = "not_pending_payment", "仅待付款订单可以改价"
		return result, nil
	}
	adjustment := adjuster.AdjustOrderPrice(ctx, order.CookieID, orderID, request.PriceCents, request.UserID)
	if adjustment.RuntimeCookieChanged {
		s.runtime.UpdateRunningCookie(ctx, order.CookieID, adjustment.RuntimeCookie)
	}
	switch {
	case adjustment.Success:
		// amount 是改价后的订单金额（元，两位小数），与订单同步写入的格式一致。
		amount := fmt.Sprintf("%d.%02d", request.PriceCents/100, request.PriceCents%100)
		if upsertErr := s.repository.UpsertOrder(ctx, orderID, UpsertOptions{CookieID: order.CookieID, Amount: amount}); upsertErr != nil {
			s.runtime.ReportPersistenceFailure(orderID, upsertErr)
		}
		result.Status, result.Message = "succeeded", "闲鱼已确认改价"
	case adjustment.Uncertain:
		result.Status, result.Message = "uncertain", "改价结果未知，请到闲鱼核对，不要重复改价"
		if adjustment.Err != nil {
			result.Message += ": " + adjustment.Err.Error()
		}
	default:
		result.Status, result.Message = "failed", "闲鱼未改价"
		if adjustment.Err != nil {
			result.Message += ": " + adjustment.Err.Error()
		} else if len(adjustment.Messages) > 0 {
			result.Message += ": " + strings.Join(adjustment.Messages, "; ")
		}
	}
	return result, nil
}
