package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	orderapp "xianyu-go/internal/application/orders"
	"xianyu-go/internal/auth"
)

// closeOrderResponse 是关闭订单成功响应。
type closeOrderResponse struct {
	// OrderID 是订单标识。
	OrderID string `json:"order_id"`
	// Status 固定为 succeeded。
	Status string `json:"status"`
	// Message 是结果说明。
	Message string `json:"message"`
}

// closeOrder 由卖家关闭一笔待付款或已付款待发货的订单（货源台 goofish 专用，逐单，只请求平台一次；已付款的平台原路退款）。
// 200 平台确认已关闭；403 无权；404 订单不存在；409 order_not_closable 本地状态不可关闭（未请求平台）；
// 502 order_close_failed 平台明确未关闭，order_close_uncertain 结果未知（不得自动重试）；503 能力未装配。
func (s *Server) closeOrder(w http.ResponseWriter, r *http.Request) {
	// orderID 是路径中的订单标识。
	orderID := strings.TrimSpace(chi.URLParam(r, "order_id"))
	if orderID == "" {
		writeErr(w, http.StatusBadRequest, "缺少订单ID")
		return
	}
	// sess 是当前登录会话。
	sess := auth.SessionFromContext(r.Context())
	// result、err 是关闭订单用例结果。
	result, err := s.orders().CloseOrder(r.Context(), orderapp.CloseOrderRequest{UserID: sess.UserID, OrderID: orderID})
	if errors.Is(err, orderapp.ErrOrderCloseUnavailable) {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "关闭订单失败")
		return
	}
	switch result.Status {
	case "succeeded":
		writeJSON(w, http.StatusOK, closeOrderResponse{OrderID: orderID, Status: result.Status, Message: result.Message})
	case "not_found":
		writeErr(w, http.StatusNotFound, result.Message)
	case "forbidden":
		writeErr(w, http.StatusForbidden, result.Message)
	case "not_closable":
		writeErrDetails(w, http.StatusConflict, "order_not_closable", result.Message, "", nil)
	case "uncertain":
		writeErrDetails(w, http.StatusBadGateway, "order_close_uncertain", result.Message, "", nil)
	default:
		writeErrDetails(w, http.StatusBadGateway, "order_close_failed", result.Message, "", nil)
	}
}
