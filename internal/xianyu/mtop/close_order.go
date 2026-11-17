// Package mtop: 卖家关闭订单域 — mtop.taobao.idle.trade.merchant.close.by.seller 调用与重试。
package mtop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"xianyu-go/internal/xianyu/protocol"
)

// CloseOrderAPI 是卖家主动关闭订单的 MTOP 端点（闲鱼卖家后台「关闭交易」）。
// 协议参照开源项目 zhinianboke/xianyu-auto-reply 的 close_order_by_seller（发货前拦截时关闭已付款订单），本仓库尚未在真实订单上验证。
const CloseOrderAPI = "https://h5api.m.goofish.com/h5/mtop.taobao.idle.trade.merchant.close.by.seller/2.0/"

// CloseOrderReason 是闲鱼卖家后台预设的关闭原因文案；平台只接受预设值。
const CloseOrderReason = "其他原因"

// closeOrderPayload 是关闭订单接口的 JSON 请求体：tid 与 bizOrderId 都是订单号。
type closeOrderPayload struct {
	// TID 是闲鱼订单号。
	TID string `json:"tid"`
	// BizOrderID 是闲鱼订单号。
	BizOrderID string `json:"bizOrderId"`
	// CloseReason 是平台预设的关闭原因。
	CloseReason string `json:"closeReason"`
}

// CloseOrderBySellerContext 调用 mtop.taobao.idle.trade.merchant.close.by.seller 由卖家关闭订单。
// 未付款订单直接关闭；已付款未发货订单关闭后平台把货款原路退给买家。
// 签名 token 过期时使用响应下发的新 Cookie 重签并重试，与关闭订单保持同一恢复策略。
func (c *ClientImpl) CloseOrderBySellerContext(ctx context.Context, cookiesStr, orderID string) (ok bool, ret []string, updatedCookies string, err error) {
	// currentCookies 是本轮请求实际使用的扁平 Cookie，优先取上下文中的会话状态。
	currentCookies := cookiesStr
	if // session 是上下文携带的响应 Cookie 会话，存在时以其合并状态为准。
	session := cookieSessionFromContext(ctx); session != nil {
		currentCookies, _, _ = session.State()
	}
	// lastRet 保存最后一次业务返回，供重试耗尽后回传调用方。
	var lastRet []string
	for // attempt 是含首次在内的 token 重签尝试序号。
	attempt := 0; attempt < 4; attempt++ {
		// previousCookies 记录本次请求前的 Cookie，用于判断响应是否下发了新 token。
		previousCookies := currentCookies
		// ok、ret、updated、requestErr 分别是单次调用的业务成功标志、业务返回、Cookie 更新和传输错误。
		ok, ret, updated, requestErr := c.closeOrderOnce(ctx, currentCookies, orderID)
		if requestErr != nil {
			if !IsMTopTokenExpiredErr(requestErr) {
				return false, ret, currentCookies, requestErr
			}
			lastRet = mtopErrorRet(requestErr)
		} else {
			lastRet = ret
			if updated != "" {
				currentCookies = updated
			}
			if ok {
				return true, ret, currentCookies, nil
			}
			requestErr = c.mtopResponseFailure("关闭订单接口", http.StatusOK, ret, "平台业务未确认成功")
			if !IsMTopTokenExpiredErr(requestErr) {
				return false, ret, currentCookies, requestErr
			}
		}
		if updated != "" {
			currentCookies = updated
		}
		if attempt == 3 {
			break
		}

		// MTop 通常会在 token 过期响应中通过 Set-Cookie 下发新签名 token；
		// 未下发时主动刷新一次 token 再重试。
		if !mtopTokenCookieChanged(previousCookies, currentCookies) {
			// refreshed、refreshErr 分别是 token 主动刷新结果和刷新失败原因。
			refreshed, refreshErr := c.RefreshTokenContext(ctx, currentCookies)
			if refreshErr != nil {
				return false, ret, currentCookies, fmt.Errorf("关闭订单 token 过期且刷新失败: %w", refreshErr)
			}
			if refreshed.UpdatedCookies != "" {
				currentCookies = refreshed.UpdatedCookies
			}
		}
		if // err 是重试间隔等待期间的上下文取消错误。
		err := sleepCtx(ctx, MTopRetryGap); err != nil {
			return false, ret, currentCookies, err
		}
	}
	return false, lastRet, currentCookies, fmt.Errorf("关闭订单接口 Token 重试失败: %w", c.mtopResponseFailure("关闭订单接口", http.StatusOK, lastRet, "重试次数已耗尽"))
}

// closeOrderOnce 执行一次关闭订单请求；业务成功要求 ret 为 SUCCESS。
func (c *ClientImpl) closeOrderOnce(ctx context.Context, cookiesStr, orderID string) (ok bool, ret []string, updatedCookies string, err error) {
	// hc 是带统一日志的 HTTP 客户端。
	hc := c.httpClient()
	// closeURL 是实际请求端点，测试可通过 CloseOrderURL 覆盖。
	closeURL := c.CloseOrderURL
	if closeURL == "" {
		closeURL = CloseOrderAPI
	}
	// signingCookies、requestCookies 分别是参与签名的 Cookie 和随请求发送的 Cookie。
	signingCookies, requestCookies := mtopRequestCookies(ctx, cookiesStr, "https://seller.goofish.com/", closeURL)
	// token 是从 Cookie 提取的 MTOP 签名 token。
	token := protocol.SignToken(signingCookies)
	// t 是签名使用的毫秒时间戳。
	t := strconv.FormatInt(time.Now().UnixMilli(), 10)
	// payload 是关闭订单请求的结构化参数，序列化结果同时作为签名原文和 HTTP 请求体。
	payload := closeOrderPayload{TID: orderID, BizOrderID: orderID, CloseReason: CloseOrderReason}
	// dataBytes、marshalErr 分别是序列化后的请求原文和理论上仅可能由未来字段变更引入的错误。
	dataBytes, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return false, nil, cookiesStr, fmt.Errorf("序列化关闭订单请求: %w", marshalErr)
	}
	// dataVal 是关闭订单请求体：tid、bizOrderId 和预设关闭原因。
	dataVal := string(dataBytes)
	// sign 是本次请求的 MTOP 签名。
	sign := protocol.GenerateSign(t, token, dataVal)

	// query 是关闭订单请求的 URL 查询参数。
	query := buildCloseOrderQuery(t, sign)
	// body 是 URL 编码后的请求体。
	body := "data=" + url.QueryEscape(dataVal)
	// req、err 分别是构造的 HTTP 请求和构造错误。
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		closeURL+"?"+query,
		strings.NewReader(body))
	if err != nil {
		return false, nil, cookiesStr, err
	}
	setCommonHeaders(req, requestCookies)
	// 卖家后台接口校验来源，origin/referer 必须是 seller.goofish.com。
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "https://seller.goofish.com")
	req.Header.Set("Referer", "https://seller.goofish.com/?site=COMMONPRO")
	req.Header.Set("idle_site_biz_code", "COMMONPRO")
	// resp、err 分别是 HTTP 响应和传输错误。
	resp, err := hc.Do(req)
	if err != nil {
		return false, nil, cookiesStr, fmt.Errorf("关闭订单请求失败: %w", err)
	}
	defer resp.Body.Close()
	// updated 是合并响应 Set-Cookie 后的最新扁平 Cookie。
	updated := absorbMTopResponseCookies(ctx, cookiesStr, resp)
	// raw、err 分别是响应正文和读取错误。
	raw, err := readMTopBody(resp)
	if err != nil {
		return false, nil, updated, c.mtopResponseFailureWithCause("关闭订单接口", resp.StatusCode, nil, "读取响应失败", err)
	}
	// res 是关闭订单响应的最小解析结构；ret 为 SUCCESS 即平台确认已关闭。
	var res struct {
		Ret []string `json:"ret"`
	}
	if // err 是响应 JSON 解析错误。
	err := json.Unmarshal(raw, &res); err != nil {
		return false, nil, updated, c.mtopResponseFailureWithCause("关闭订单接口", resp.StatusCode, nil, "JSON 解析失败", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return false, res.Ret, updated, c.mtopResponseFailure("关闭订单接口", resp.StatusCode, res.Ret, "HTTP 状态异常")
	}
	if hasMTopSuccess(res.Ret) {
		return true, res.Ret, updated, nil
	}
	return false, res.Ret, updated, nil
}

// buildCloseOrderQuery 构造关闭订单请求的固定查询参数。
func buildCloseOrderQuery(t, sign string) string {
	// parts 是按顺序拼接的查询键值对。
	parts := [][2]string{
		{"jsv", "2.7.2"},
		{"appKey", protocol.SignAppKey},
		{"t", t},
		{"sign", sign},
		{"v", "2.0"},
		{"type", "originaljson"},
		{"accountSite", "xianyu"},
		{"dataType", "json"},
		{"timeout", "20000"},
		{"api", "mtop.taobao.idle.trade.merchant.close.by.seller"},
		{"sessionOption", "AutoLoginOnly"},
	}
	// b 是查询字符串构造器。
	var b strings.Builder
	// i、p 分别是当前键值对的下标和内容。
	for i, p := range parts {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p[0])
		b.WriteByte('=')
		b.WriteString(p[1])
	}
	return b.String()
}
