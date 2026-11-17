package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	orderapp "xianyu-go/internal/application/orders"
	"xianyu-go/internal/auth"
)

// adjustOrderPriceRequest 是待付款订单改价请求：price_cents 为改价后的订单总价（分）。
type adjustOrderPriceRequest struct {
	// PriceCents 是改价后的订单总价，单位为分。
	PriceCents int64 `json:"price_cents"`
}

// adjustOrderPriceResponse 是改价成功响应。
type adjustOrderPriceResponse struct {
	// OrderID 是订单标识。
	OrderID string `json:"order_id"`
	// Status 固定为 succeeded。
	Status string `json:"status"`
	// PriceCents 是改价后的订单总价（分）。
	PriceCents int64 `json:"price_cents"`
	// Message 是结果说明。
	Message string `json:"message"`
}

// adjustOrderPrice 把一笔待付款订单改成新总价（货源台 goofish 专用，逐单，只请求平台一次）。
// 200 平台确认已改；400 参数错误；403 无权；404 订单不存在；409 本地订单不是待付款（未请求平台）；
// 502 order_price_failed 平台明确未改，order_price_uncertain 结果未知（不得自动重试）；503 能力未装配。
func (s *Server) adjustOrderPrice(w http.ResponseWriter, r *http.Request) {
	// orderID 是路径中的订单标识。
	orderID := strings.TrimSpace(chi.URLParam(r, "order_id"))
	// req 是请求体。
	var req adjustOrderPriceRequest
	if err := decodeJSON(r, &req); err != nil || orderID == "" || req.PriceCents <= 0 {
		writeErr(w, http.StatusBadRequest, "price_cents 必须是大于 0 的整数（分）")
		return
	}
	// sess 是当前登录会话。
	sess := auth.SessionFromContext(r.Context())
	// result、err 是改价用例结果。
	result, err := s.orders().AdjustPrice(r.Context(), orderapp.AdjustPriceRequest{UserID: sess.UserID, OrderID: orderID, PriceCents: req.PriceCents})
	if errors.Is(err, orderapp.ErrPriceAdjustUnavailable) {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "订单改价失败")
		return
	}
	switch result.Status {
	case "succeeded":
		writeJSON(w, http.StatusOK, adjustOrderPriceResponse{OrderID: orderID, Status: result.Status, PriceCents: req.PriceCents, Message: result.Message})
	case "not_found":
		writeErr(w, http.StatusNotFound, result.Message)
	case "forbidden":
		writeErr(w, http.StatusForbidden, result.Message)
	case "not_pending_payment":
		writeErrDetails(w, http.StatusConflict, "order_not_pending_payment", result.Message, "", nil)
	case "uncertain":
		writeErrDetails(w, http.StatusBadGateway, "order_price_uncertain", result.Message, "", nil)
	default:
		writeErrDetails(w, http.StatusBadGateway, "order_price_failed", result.Message, "", nil)
	}
}
