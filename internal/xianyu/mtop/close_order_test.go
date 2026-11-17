package mtop

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestCloseOrderBySellerSendsSellerOriginAndPresetReason 验证关闭订单请求的接口、版本、来源和请求体。
func TestCloseOrderBySellerSendsSellerOriginAndPresetReason(t *testing.T) {
	var gotQuery url.Values
	var gotBody, gotOrigin string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		body, _ := io.ReadAll(r.Body)
		gotBody, gotOrigin = string(body), r.Header.Get("Origin")
		fmt.Fprint(w, `{"api":"mtop.taobao.idle.trade.merchant.close.by.seller","ret":["SUCCESS::调用成功"],"data":{},"v":"2.0"}`)
	}))
	defer server.Close()
	client := &ClientImpl{HTTPClient: server.Client(), CloseOrderURL: server.URL + "/"}

	ok, _, _, err := client.CloseOrderBySellerContext(context.Background(), "unb=123; _m_h5_tk=token_1;", "5127615830026003820")

	if err != nil || !ok {
		t.Fatalf("关闭订单应成功: ok=%v err=%v", ok, err)
	}
	if gotQuery.Get("api") != "mtop.taobao.idle.trade.merchant.close.by.seller" || gotQuery.Get("v") != "2.0" || gotOrigin != "https://seller.goofish.com" {
		t.Fatalf("请求参数异常: query=%v origin=%q", gotQuery, gotOrigin)
	}
	data, _ := url.QueryUnescape(strings.TrimPrefix(gotBody, "data="))
	if data != `{"tid":"5127615830026003820","bizOrderId":"5127615830026003820","closeReason":"其他原因"}` {
		t.Fatalf("请求体异常: %s", data)
	}
}

// TestCloseOrderBySellerRefused 验证平台业务拒绝返回未成功和原始 ret。
func TestCloseOrderBySellerRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ret":["FAIL_BIZ_ORDER_STATUS_ERROR::订单状态不允许关闭"],"data":{}}`)
	}))
	defer server.Close()
	client := &ClientImpl{HTTPClient: server.Client(), CloseOrderURL: server.URL + "/"}
	ok, ret, _, err := client.CloseOrderBySellerContext(context.Background(), "unb=123; _m_h5_tk=token_1;", "1")
	if ok || err == nil || len(ret) == 0 {
		t.Fatalf("业务拒绝应返回失败: ok=%v ret=%v err=%v", ok, ret, err)
	}
}
