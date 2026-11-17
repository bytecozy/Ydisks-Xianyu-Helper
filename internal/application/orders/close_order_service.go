package orders

import (
	"context"
	"errors"
	"strings"
)

// ErrOrderCloseUnavailable 表示订单运行时没有装配卖家关闭订单能力。
var ErrOrderCloseUnavailable = errors.New("关闭订单能力未初始化")

// CloseOrderRequest 描述卖家关闭一笔订单的请求（货源台 goofish 专用，逐单）。
type CloseOrderRequest struct {
	// UserID 是当前登录用户标识。
	UserID int64
	// OrderID 是订单标识。
	OrderID string
}

// OrderCloser 是订单运行时可选实现的卖家关闭订单能力。
type OrderCloser interface {
	// CloseOrderBySeller 调用平台由卖家关闭订单；结果沿用改价的成功/不确定/失败约定。
	CloseOrderBySeller(ctx context.Context, cookieID, orderID string, userID int64) PriceAdjustment
}

// CloseOrder 关闭当前用户账号下一笔待付款（processing）或已付款待发货（pending_ship）的订单；已付款的由平台原路退款。
// 只请求平台一次，结果不确定时不重试。Status 为 succeeded、failed、uncertain、not_closable（本地状态不可关闭，未请求平台）、
// not_found、forbidden。
func (s *ManualShipService) CloseOrder(ctx context.Context, request CloseOrderRequest) (AdjustPriceResult, error) {
	if s == nil || s.repository == nil || s.runtime == nil {
		return AdjustPriceResult{}, errors.New("关闭订单依赖未初始化")
	}
	// closer 是运行时的关闭订单能力；未装配时由 HTTP 层返回 503。
	closer, ok := s.runtime.(OrderCloser)
	if !ok || !s.runtime.MTopAvailable() {
		return AdjustPriceResult{}, ErrOrderCloseUnavailable
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
	if status := NormalizeOrderStatus(strings.TrimSpace(order.OrderStatus)); status != "processing" && status != "pending_ship" {
		result.Status, result.Message = "not_closable", "仅待付款或待发货订单可以关闭（当前 "+status+"）"
		return result, nil
	}
	closing := closer.CloseOrderBySeller(ctx, order.CookieID, orderID, request.UserID)
	if closing.RuntimeCookieChanged {
		s.runtime.UpdateRunningCookie(ctx, order.CookieID, closing.RuntimeCookie)
	}
	switch {
	case closing.Success:
		if upsertErr := s.repository.UpsertOrder(ctx, orderID, UpsertOptions{CookieID: order.CookieID, OrderStatus: "cancelled"}); upsertErr != nil {
			s.runtime.ReportPersistenceFailure(orderID, upsertErr)
		}
		result.Status, result.Message = "succeeded", "闲鱼已关闭订单"
	case closing.Uncertain:
		result.Status, result.Message = "uncertain", "关闭结果未知，请到闲鱼核对，不要重复关闭"
		if closing.Err != nil {
			result.Message += ": " + closing.Err.Error()
		}
	default:
		result.Status, result.Message = "failed", "闲鱼未关闭订单"
		if closing.Err != nil {
			result.Message += ": " + closing.Err.Error()
		} else if len(closing.Messages) > 0 {
			result.Message += ": " + strings.Join(closing.Messages, "; ")
		}
	}
	return result, nil
}
