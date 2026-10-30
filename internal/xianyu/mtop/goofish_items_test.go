package mtop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// goofishTestCookies 是带签名令牌和 unb 的扁平 Cookie。
const goofishTestCookies = "_m_h5_tk=abc_123; _m_h5_tk_enc=enc; unb=2200000001; cookie2=c2"

// goofishCall 记录一次被替身接住的请求。
type goofishRecorded struct {
	api  string
	data map[string]any
}

// goofishStub 是按 API 名称分发的 RoundTripper 替身。
type goofishStub struct {
	mu       sync.Mutex
	calls    []goofishRecorded
	uploads  int
	handlers map[string]func(n int, data map[string]any, req *http.Request) (*http.Response, error)
	counts   map[string]int
}

func newGoofishStub() *goofishStub {
	return &goofishStub{handlers: map[string]func(int, map[string]any, *http.Request) (*http.Response, error){}, counts: map[string]int{}}
}

func (s *goofishStub) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Host, "stream-upload") {
		s.mu.Lock()
		s.uploads++
		n := s.uploads
		s.mu.Unlock()
		if h := s.handlers["upload"]; h != nil {
			return h(n, nil, req)
		}
		return goofishJSON(req, 200, `{"success":true,"object":{"url":"https://img.alicdn.com/up`+string(rune('0'+n))+`.jpg","pix":"800x600"}}`), nil
	}
	api := req.URL.Query().Get("api")
	body, _ := io.ReadAll(req.Body)
	form, _ := url.ParseQuery(string(body))
	var data map[string]any
	_ = json.Unmarshal([]byte(form.Get("data")), &data)
	s.mu.Lock()
	s.calls = append(s.calls, goofishRecorded{api: api, data: data})
	s.counts[api]++
	n := s.counts[api]
	h := s.handlers[api]
	s.mu.Unlock()
	if trace := httptrace.ContextClientTrace(req.Context()); trace != nil && trace.WroteRequest != nil && !strings.HasSuffix(api, "#nowrite") {
		trace.WroteRequest(httptrace.WroteRequestInfo{})
	}
	if h == nil {
		return goofishJSON(req, 200, `{"ret":["SUCCESS::调用成功"],"data":{}}`), nil
	}
	return h(n, data, req)
}

func (s *goofishStub) last(api string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.calls) - 1; i >= 0; i-- {
		if s.calls[i].api == api {
			return s.calls[i].data
		}
	}
	return nil
}

func goofishJSON(req *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

func goofishClient(stub *goofishStub) *ClientImpl {
	client := NewClient()
	client.HTTPClient = &http.Client{Transport: stub}
	return client
}

// goofishTemplateDetail 是模拟的模板商品 editDetail（字段形状来自 PC 编辑页抓包记录）。
const goofishTemplateDetail = `{"ret":["SUCCESS::调用成功"],"data":{
 "itemId":"900000000001","itemStatus":"0","simpleItem":"true","quantity":"1","itemTypeStr":"b","freebies":"false",
 "itemTextDTO":{"title":"旧标题","desc":"旧标题\n\n旧正文","titleDescSeparate":"false"},
 "itemPriceDTO":{"priceInCent":"12000","origPriceInCent":"0"},
 "itemPostFeeDTO":{"canFreeShipping":"false","supportFreight":"false","onlyTakeSelf":"false","templateId":"0"},
 "itemCatDTO":{"catId":"50025461","catName":"游戏账号","channelCatId":"201453616","tbCatId":"50018588"},
 "itemLabelExtList":[{"propertyName":"分类","text":"成品号","properties":"p1##分类:v1##成品号"},{"propertyName":"游戏名称","text":"原神"},{"propertyName":"客户端","text":"全平台互通"}],
 "userRightsProtocols":[{"serviceCode":"SKILL_PLAY_NO_MIND","enable":"false"}],
 "imageInfoDOList":[{"url":"https://img.alicdn.com/old1.jpg","major":true,"widthSize":800,"heightSize":800}],
 "itemAddrDTO":{"prov":"浙江省","city":"杭州市","area":"余杭区","divisionId":"330110","gps":"30.1,120.1"},
 "redirectUrl":"","jumpUrl":""}}`

func goofishImagesN(n int) []PublishImage {
	out := make([]PublishImage, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, PublishImage{Filename: "a.jpg", ContentType: "image/jpeg", Data: []byte{0xff, 0xd8, 0xff}})
	}
	return out
}

func TestGoofishPublishFromTemplateMapsEditDetail(t *testing.T) {
	stub := newGoofishStub()
	stub.handlers[GoofishEditDetailAPI] = func(_ int, _ map[string]any, req *http.Request) (*http.Response, error) {
		return goofishJSON(req, 200, goofishTemplateDetail), nil
	}
	stub.handlers[GoofishPublishAPI] = func(_ int, _ map[string]any, req *http.Request) (*http.Response, error) {
		return goofishJSON(req, 200, `{"ret":["SUCCESS::调用成功"],"data":{"itemId":"900000000777"}}`), nil
	}
	paced := 0
	res, err := goofishClient(stub).GoofishPublishFromTemplate(context.Background(), goofishTestCookies, GoofishPublishRequest{
		TemplateItemID: "900000000001", Title: "✅ 原神 满命胡桃", Description: "详情第一行\n第二行", PriceCents: 8850,
		Images: goofishImagesN(2), Pace: func(context.Context) error { paced++; return nil },
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.ItemID != "900000000777" || res.ItemURL != "https://www.goofish.com/item?id=900000000777" {
		t.Fatalf("result = %+v", res)
	}
	if paced != 4 { // editDetail + 2 uploads + publish
		t.Fatalf("pace calls = %d, want 4", paced)
	}
	if got := stub.last(GoofishEditDetailAPI); got["itemId"] != "900000000001" {
		t.Fatalf("editDetail data = %#v", got)
	}
	data := stub.last(GoofishPublishAPI)
	if _, ok := data["itemId"]; ok {
		t.Fatal("publish 不能带模板 itemId")
	}
	cat := data["itemCatDTO"].(map[string]any)
	if cat["catId"] != "50025461" || cat["channelCatId"] != "201453616" || cat["tbCatId"] != "50018588" {
		t.Fatalf("itemCatDTO = %#v", cat)
	}
	if labels := data["itemLabelExtList"].([]any); len(labels) != 3 || labels[0].(map[string]any)["text"] != "成品号" {
		t.Fatalf("labels = %#v", labels)
	}
	post := data["itemPostFeeDTO"].(map[string]any)
	if post["canFreeShipping"] != false || post["supportFreight"] != false || post["templateId"] != "0" {
		t.Fatalf("itemPostFeeDTO = %#v", post)
	}
	if addr := data["itemAddrDTO"].(map[string]any); addr["divisionId"] != "330110" {
		t.Fatalf("itemAddrDTO = %#v", addr)
	}
	text := data["itemTextDTO"].(map[string]any)
	if text["title"] != "✅ 原神 满命胡桃" || text["desc"] != "✅ 原神 满命胡桃\n\n详情第一行\n第二行" || text["titleDescSeparate"] != false {
		t.Fatalf("itemTextDTO = %#v", text)
	}
	if price := data["itemPriceDTO"].(map[string]any); price["priceInCent"] != "8850" || len(price) != 1 {
		t.Fatalf("itemPriceDTO = %#v", price)
	}
	images := data["imageInfoDOList"].([]any)
	if len(images) != 2 || images[0].(map[string]any)["major"] != true || images[1].(map[string]any)["major"] != false || images[0].(map[string]any)["url"] != "https://img.alicdn.com/up1.jpg" {
		t.Fatalf("images = %#v", images)
	}
	if data["quantity"] != "1" || data["simpleItem"] != "true" || data["freebies"] != false || data["sourceId"] != "pcMainPublish" || data["publishScene"] != "pcMainPublish" {
		t.Fatalf("scalars = %#v", data)
	}
	if protocols := data["userRightsProtocols"].([]any); protocols[0].(map[string]any)["enable"] != false {
		t.Fatalf("protocols = %#v", protocols)
	}
}

func goofishKind(t *testing.T, err error) GoofishErrorKind {
	t.Helper()
	var gerr *GoofishError
	if !errors.As(err, &gerr) {
		t.Fatalf("err = %#v, want *GoofishError", err)
	}
	return gerr.Kind
}

func TestGoofishPublishErrorClassification(t *testing.T) {
	cases := []struct {
		name    string
		publish func(*http.Request) (*http.Response, error)
		want    GoofishErrorKind
		message string
	}{
		{name: "business rejection", publish: func(r *http.Request) (*http.Response, error) {
			return goofishJSON(r, 200, `{"ret":["FAIL_BIZ_ITEM_TITLE_ILLEGAL::标题包含违规内容"],"data":{}}`), nil
		}, want: GoofishErrRejected, message: "标题包含违规内容"},
		{name: "custom business code", publish: func(r *http.Request) (*http.Response, error) {
			return goofishJSON(r, 200, `{"ret":["IDLE_PUBLISH_LIMIT::今日发布数量已达上限"],"data":{}}`), nil
		}, want: GoofishErrRejected, message: "今日发布数量已达上限"},
		{name: "system error", publish: func(r *http.Request) (*http.Response, error) {
			return goofishJSON(r, 200, `{"ret":["FAIL_SYS_SERVICE_UNAVAILABLE::服务不可用"]}`), nil
		}, want: GoofishErrUncertain},
		{name: "gateway flow limit", publish: func(r *http.Request) (*http.Response, error) {
			return goofishJSON(r, 200, `{"ret":["FAIL_SYS_FLOWLIMIT::哎哟喂"]}`), nil
		}, want: GoofishErrRejected},
		{name: "http 502", publish: func(r *http.Request) (*http.Response, error) {
			return goofishJSON(r, 502, `{"ret":[]}`), nil
		}, want: GoofishErrUncertain},
		{name: "non json 504", publish: func(r *http.Request) (*http.Response, error) {
			return goofishJSON(r, 504, `<html>gateway timeout</html>`), nil
		}, want: GoofishErrUncertain},
		{name: "connection reset after write", publish: func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("connection reset by peer")
		}, want: GoofishErrUncertain},
		{name: "success without item id", publish: func(r *http.Request) (*http.Response, error) {
			return goofishJSON(r, 200, `{"ret":["SUCCESS::调用成功"],"data":{}}`), nil
		}, want: GoofishErrUncertain},
		{name: "rgv587", publish: func(r *http.Request) (*http.Response, error) {
			return goofishJSON(r, 200, `{"ret":["RGV587_ERROR::SM::哎哟喂,被挤爆啦"],"data":{"url":"https://punish.example/captcha"}}`), nil
		}, want: GoofishErrRisk},
		{name: "punish html", publish: func(r *http.Request) (*http.Response, error) {
			return goofishJSON(r, 200, `<html><script>window.location='https://h5api.m.goofish.com/punish?x5secdata=xx'</script></html>`), nil
		}, want: GoofishErrRisk},
		{name: "user validate", publish: func(r *http.Request) (*http.Response, error) {
			return goofishJSON(r, 200, `{"ret":["FAIL_SYS_USER_VALIDATE::请完成验证"]}`), nil
		}, want: GoofishErrRisk},
		{name: "session expired", publish: func(r *http.Request) (*http.Response, error) {
			return goofishJSON(r, 200, `{"ret":["FAIL_SYS_SESSION_EXPIRED::Session过期"]}`), nil
		}, want: GoofishErrSessionExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newGoofishStub()
			stub.handlers[GoofishEditDetailAPI] = func(_ int, _ map[string]any, req *http.Request) (*http.Response, error) {
				return goofishJSON(req, 200, goofishTemplateDetail), nil
			}
			stub.handlers[GoofishPublishAPI] = func(_ int, _ map[string]any, req *http.Request) (*http.Response, error) { return tc.publish(req) }
			_, err := goofishClient(stub).GoofishPublishFromTemplate(context.Background(), goofishTestCookies, GoofishPublishRequest{
				TemplateItemID: "1", Title: "t", PriceCents: 100, Images: goofishImagesN(1),
			})
			if got := goofishKind(t, err); got != tc.want {
				t.Fatalf("kind = %s, want %s (err=%v)", got, tc.want, err)
			}
			if tc.message != "" && !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("message %q 不含闲鱼原文 %q", err.Error(), tc.message)
			}
			if stub.counts[GoofishPublishAPI] != 1 {
				t.Fatalf("publish 调用 %d 次，风控/拒绝/不明都不能重试", stub.counts[GoofishPublishAPI])
			}
		})
	}
}

func TestGoofishTransportErrorBeforeWriteIsRejected(t *testing.T) {
	stub := newGoofishStub()
	client := goofishClient(stub)
	client.HTTPClient = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: connection refused")
	})}
	err := client.GoofishDownshelf(context.Background(), goofishTestCookies, "1", nil)
	if got := goofishKind(t, err); got != GoofishErrRejected {
		t.Fatalf("kind = %s, want rejected（请求没写出）", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGoofishTokenExpiredIsResignedAndRetried(t *testing.T) {
	stub := newGoofishStub()
	stub.handlers[GoofishDownshelfAPI] = func(n int, _ map[string]any, req *http.Request) (*http.Response, error) {
		if n == 1 {
			resp := goofishJSON(req, 200, `{"ret":["FAIL_SYS_TOKEN_EXOIRED::令牌过期"]}`)
			resp.Header.Add("Set-Cookie", "_m_h5_tk=new_456; Path=/; Domain=.goofish.com")
			return resp, nil
		}
		return goofishJSON(req, 200, `{"ret":["SUCCESS::调用成功"],"data":{"success":true}}`), nil
	}
	if err := goofishClient(stub).GoofishDownshelf(context.Background(), goofishTestCookies, "42", nil); err != nil {
		t.Fatalf("downshelf: %v", err)
	}
	if stub.counts[GoofishDownshelfAPI] != 2 {
		t.Fatalf("downshelf 调用 %d 次，want 2", stub.counts[GoofishDownshelfAPI])
	}
	if got := stub.last(GoofishDownshelfAPI); got["itemId"] != "42" || len(got) != 1 {
		t.Fatalf("downshelf data = %#v", got)
	}
}

func TestGoofishTemplateProblemsAreInvalid(t *testing.T) {
	cases := map[string]string{
		"not found": `{"ret":["FAIL_BIZ_ITEM_NOT_EXIST::商品不存在"]}`,
		"multi sku": strings.Replace(goofishTemplateDetail, `"redirectUrl":""`, `"itemSkuList":[{"priceInCent":"100"}],"redirectUrl":""`, 1),
		"no cat":    strings.Replace(goofishTemplateDetail, `"catId":"50025461"`, `"catId":""`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			stub := newGoofishStub()
			stub.handlers[GoofishEditDetailAPI] = func(_ int, _ map[string]any, req *http.Request) (*http.Response, error) {
				return goofishJSON(req, 200, body), nil
			}
			_, err := goofishClient(stub).GoofishPublishFromTemplate(context.Background(), goofishTestCookies, GoofishPublishRequest{TemplateItemID: "1", Title: "t", PriceCents: 100, Images: goofishImagesN(1)})
			if got := goofishKind(t, err); got != GoofishErrInvalid {
				t.Fatalf("kind = %s, want invalid", got)
			}
			if stub.uploads != 0 || stub.counts[GoofishPublishAPI] != 0 {
				t.Fatal("模板不可用时不能上传图片或发布")
			}
		})
	}
}

func TestGoofishUploadFailureMeansNotPublished(t *testing.T) {
	stub := newGoofishStub()
	stub.handlers[GoofishEditDetailAPI] = func(_ int, _ map[string]any, req *http.Request) (*http.Response, error) {
		return goofishJSON(req, 200, goofishTemplateDetail), nil
	}
	stub.handlers["upload"] = func(n int, _ map[string]any, req *http.Request) (*http.Response, error) {
		if n == 2 {
			return goofishJSON(req, 500, `oops`), nil
		}
		return goofishJSON(req, 200, `{"object":{"url":"https://img.alicdn.com/x.jpg","pix":"1x1"}}`), nil
	}
	_, err := goofishClient(stub).GoofishPublishFromTemplate(context.Background(), goofishTestCookies, GoofishPublishRequest{TemplateItemID: "1", Title: "t", PriceCents: 100, Images: goofishImagesN(3)})
	if got := goofishKind(t, err); got != GoofishErrRejected || !strings.Contains(err.Error(), "第 2 张") {
		t.Fatalf("kind = %s err=%v", got, err)
	}
	if stub.counts[GoofishPublishAPI] != 0 {
		t.Fatal("上传失败后不能发布")
	}
}

func TestGoofishEditPayload(t *testing.T) {
	detail := strings.Replace(goofishTemplateDetail, `"origPriceInCent":"0"`, `"origPriceInCent":"9000"`, 1)
	stub := newGoofishStub()
	stub.handlers[GoofishEditDetailAPI] = func(_ int, _ map[string]any, req *http.Request) (*http.Response, error) {
		return goofishJSON(req, 200, detail), nil
	}
	title, price := "新标题", int64(9900)
	res, err := goofishClient(stub).GoofishEditItem(context.Background(), goofishTestCookies, GoofishEditRequest{
		ItemID: "900000000001", Title: &title, PriceCents: &price, Images: goofishImagesN(2),
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	data := stub.last(GoofishEditAPI)
	if data["itemId"] != "900000000001" || data["bizcode"] != "pcMainPublish" || data["publishScene"] != "pcMainPublish" || data["sourceId"] != "pcMainPublish" || data["uniqueCode"] == "" {
		t.Fatalf("ids = %#v", data)
	}
	if _, ok := data["redirectUrl"]; ok {
		t.Fatal("edit 只能带白名单字段")
	}
	text := data["itemTextDTO"].(map[string]any)
	if text["title"] != "新标题" || text["desc"] != "新标题\n\n旧正文" || text["titleDescSeparate"] != false {
		t.Fatalf("itemTextDTO = %#v", text)
	}
	priceDTO := data["itemPriceDTO"].(map[string]any)
	if priceDTO["priceInCent"] != "9900" {
		t.Fatalf("itemPriceDTO = %#v", priceDTO)
	}
	if _, ok := priceDTO["origPriceInCent"]; ok {
		t.Fatal("原价低于新售价时应去掉")
	}
	images := data["imageInfoDOList"].([]any)
	if len(images) != 2 || images[0].(map[string]any)["url"] != "https://img.alicdn.com/up1.jpg" {
		t.Fatalf("images = %#v", images)
	}
	if cat := data["itemCatDTO"].(map[string]any); cat["catId"] != "50025461" {
		t.Fatalf("cat = %#v", cat)
	}
	if labels := data["itemLabelExtList"].([]any); len(labels) != 3 {
		t.Fatalf("labels = %#v", labels)
	}
	if post := data["itemPostFeeDTO"].(map[string]any); post["supportFreight"] != false {
		t.Fatalf("post = %#v", post)
	}
	if res.Title != "新标题" || res.PriceCents != 9900 || res.ImageURL != "https://img.alicdn.com/up1.jpg" {
		t.Fatalf("result = %+v", res)
	}
}

func TestGoofishEditKeepsUntouchedFields(t *testing.T) {
	stub := newGoofishStub()
	stub.handlers[GoofishEditDetailAPI] = func(_ int, _ map[string]any, req *http.Request) (*http.Response, error) {
		return goofishJSON(req, 200, goofishTemplateDetail), nil
	}
	body := "新正文"
	if _, err := goofishClient(stub).GoofishEditItem(context.Background(), goofishTestCookies, GoofishEditRequest{ItemID: "9", Description: &body}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	data := stub.last(GoofishEditAPI)
	if text := data["itemTextDTO"].(map[string]any); text["title"] != "旧标题" || text["desc"] != "旧标题\n\n新正文" {
		t.Fatalf("text = %#v", text)
	}
	if price := data["itemPriceDTO"].(map[string]any); price["priceInCent"] != "12000" {
		t.Fatalf("price = %#v", price)
	}
	if images := data["imageInfoDOList"].([]any); len(images) != 1 || images[0].(map[string]any)["url"] != "https://img.alicdn.com/old1.jpg" {
		t.Fatalf("images = %#v", images)
	}
	if stub.uploads != 0 {
		t.Fatal("没给图片不应上传")
	}
}

func TestGoofishEditNotFoundAndUncertain(t *testing.T) {
	stub := newGoofishStub()
	stub.handlers[GoofishEditDetailAPI] = func(_ int, _ map[string]any, req *http.Request) (*http.Response, error) {
		return goofishJSON(req, 200, `{"ret":["FAIL_BIZ_ITEM_NOT_EXIST::宝贝不存在"]}`), nil
	}
	title := "x"
	_, err := goofishClient(stub).GoofishEditItem(context.Background(), goofishTestCookies, GoofishEditRequest{ItemID: "9", Title: &title})
	if got := goofishKind(t, err); got != GoofishErrNotFound {
		t.Fatalf("kind = %s", got)
	}

	stub = newGoofishStub()
	stub.handlers[GoofishEditDetailAPI] = func(_ int, _ map[string]any, req *http.Request) (*http.Response, error) {
		return goofishJSON(req, 503, `{"ret":["FAIL_SYS_SERVICE_UNAVAILABLE::x"]}`), nil
	}
	_, err = goofishClient(stub).GoofishEditItem(context.Background(), goofishTestCookies, GoofishEditRequest{ItemID: "9", Title: &title})
	if got := goofishKind(t, err); got != GoofishErrRejected {
		t.Fatalf("editDetail 失败时编辑请求还没发，kind = %s want rejected", got)
	}

	stub = newGoofishStub()
	stub.handlers[GoofishEditDetailAPI] = func(_ int, _ map[string]any, req *http.Request) (*http.Response, error) {
		return goofishJSON(req, 200, goofishTemplateDetail), nil
	}
	stub.handlers[GoofishEditAPI] = func(_ int, _ map[string]any, req *http.Request) (*http.Response, error) {
		return goofishJSON(req, 200, `{"ret":["FAIL_SYS_HSF_ASYNC_TIMEOUT::超时"]}`), nil
	}
	_, err = goofishClient(stub).GoofishEditItem(context.Background(), goofishTestCookies, GoofishEditRequest{ItemID: "9", Title: &title})
	if got := goofishKind(t, err); got != GoofishErrUncertain {
		t.Fatalf("kind = %s want uncertain", got)
	}
}

func TestGoofishDownshelfClassification(t *testing.T) {
	cases := []struct {
		body string
		want GoofishErrorKind
	}{
		{`{"ret":["SUCCESS::调用成功"],"data":{"success":true}}`, ""},
		{`{"ret":["FAIL_BIZ_ITEM_STATUS_ERROR::宝贝已下架"]}`, GoofishErrAlreadyOffShelf},
		{`{"ret":["FAIL_BIZ_ITEM_NOT_EXIST::宝贝不存在"]}`, GoofishErrNotFound},
		{`{"ret":["FAIL_BIZ_NO_PERMISSION::操作失败"]}`, GoofishErrRejected},
		{`{"ret":["SUCCESS::调用成功"],"data":{"success":false,"errorMsg":"操作过于频繁"}}`, GoofishErrRejected},
		{`{"ret":["FAIL_SYS_SERVICE_TIMEOUT::超时"]}`, GoofishErrUncertain},
	}
	for _, tc := range cases {
		stub := newGoofishStub()
		stub.handlers[GoofishDownshelfAPI] = func(_ int, _ map[string]any, req *http.Request) (*http.Response, error) {
			return goofishJSON(req, 200, tc.body), nil
		}
		err := goofishClient(stub).GoofishDownshelf(context.Background(), goofishTestCookies, "42", nil)
		if tc.want == "" {
			if err != nil {
				t.Fatalf("%s: %v", tc.body, err)
			}
			continue
		}
		if got := goofishKind(t, err); got != tc.want {
			t.Fatalf("%s: kind = %s want %s", tc.body, got, tc.want)
		}
	}
}

func TestGoofishOnSalePage(t *testing.T) {
	stub := newGoofishStub()
	stub.handlers[GoofishItemListAPI] = func(_ int, _ map[string]any, req *http.Request) (*http.Response, error) {
		return goofishJSON(req, 200, `{"ret":["SUCCESS::调用成功"],"data":{"nextPage":true,"cardList":[
			{"cardType":1003,"cardData":{"id":"11","title":"商品一","itemStatus":0,"priceInfo":{"price":"88.5"},"detailParams":{"itemId":"11"}}},
			{"cardType":1003,"cardData":{"id":"12","title":"商品二","itemStatus":1,"priceInfo":{"price":"10"},"detailParams":{"itemId":"12"}}}]}}`), nil
	}
	page, err := goofishClient(stub).GoofishOnSalePage(context.Background(), goofishTestCookies, 2, nil)
	if err != nil {
		t.Fatalf("on sale: %v", err)
	}
	if !page.HasMore || len(page.Items) != 2 || page.Items[0] != (GoofishOnSaleItem{ItemID: "11", Title: "商品一", Price: "88.5", Status: "on_sale"}) || page.Items[1].Status != "item_status_1" {
		t.Fatalf("page = %+v", page)
	}
	data := stub.last(GoofishItemListAPI)
	if data["userId"] != "2200000001" || data["groupName"] != "在售" || data["pageNumber"] != float64(2) {
		t.Fatalf("data = %#v", data)
	}
}

func TestGoofishComposeAndSplitText(t *testing.T) {
	if got := goofishComposeText("标题", "正文"); got != "标题\n\n正文" {
		t.Fatalf("compose = %q", got)
	}
	if got := goofishComposeText("标题", "标题\n\n正文"); got != "标题\n\n正文" {
		t.Fatalf("compose dup = %q", got)
	}
	if got := goofishComposeText("标题", "  "); got != "标题" {
		t.Fatalf("compose empty = %q", got)
	}
	if title, body := goofishSplitText("", "第一行\n\n正文\n第三行"); title != "第一行" || body != "正文\n第三行" {
		t.Fatalf("split = %q %q", title, body)
	}
	if title, body := goofishSplitText("别的标题", "第一行\n正文"); title != "别的标题" || body != "第一行\n正文" {
		t.Fatalf("split mismatch = %q %q", title, body)
	}
}

func TestGoofishPaceErrorStopsBeforeSending(t *testing.T) {
	stub := newGoofishStub()
	err := goofishClient(stub).GoofishDownshelf(context.Background(), goofishTestCookies, "42", func(context.Context) error { return context.DeadlineExceeded })
	if got := goofishKind(t, err); got != GoofishErrRejected {
		t.Fatalf("kind = %s", got)
	}
	if len(stub.calls) != 0 {
		t.Fatal("节流失败时不能发请求")
	}
}

func TestGoofishSeparateTitleKeepsDescBodyOnly(t *testing.T) {
	stub := newGoofishStub()
	stub.handlers[GoofishEditDetailAPI] = func(_ int, _ map[string]any, req *http.Request) (*http.Response, error) {
		return goofishJSON(req, 200, strings.Replace(goofishTemplateDetail, `"desc":"旧标题\n\n旧正文","titleDescSeparate":"false"`, `"desc":"旧正文","titleDescSeparate":"true"`, 1)), nil
	}
	title := "新标题"
	if _, err := goofishClient(stub).GoofishEditItem(context.Background(), goofishTestCookies, GoofishEditRequest{ItemID: "9", Title: &title}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if text := stub.last(GoofishEditAPI)["itemTextDTO"].(map[string]any); text["title"] != "新标题" || text["desc"] != "旧正文" || text["titleDescSeparate"] != true {
		t.Fatalf("text = %#v", text)
	}
}

func TestGoofishPolishClassification(t *testing.T) {
	cases := []struct {
		name        string
		primary     string
		backup      string
		wantAlready bool
		want        GoofishErrorKind
		wantPrimary int
		wantBackup  int
	}{
		{"主接口成功", `{"ret":["SUCCESS::调用成功"],"data":{}}`, "", false, "", 1, 0},
		{"今天已擦亮", `{"ret":["FAIL_BIZ_IDLEITEM_POLISH_AGAIN::一天只能擦亮一次"]}`, "", true, "", 1, 0},
		{"data.success=false 已擦亮", `{"ret":["SUCCESS::调用成功"],"data":{"success":false,"errorMsg":"宝贝已经擦亮过了"}}`, "", true, "", 1, 0},
		{"主接口拒绝备用成功", `{"ret":["FAIL_SYS_API_NOT_FOUNDED::API不存在"]}`, `{"ret":["SUCCESS::调用成功"],"data":{}}`, false, "", 1, 1},
		{"备用说已擦亮", `{"ret":["FAIL_BIZ_X::失败"]}`, `{"ret":["FAIL_BIZ_POLISH_DUPLICATE::已经擦亮"]}`, true, "", 1, 1},
		{"商品不在售", `{"ret":["FAIL_BIZ_ITEM_STATUS_ERROR::宝贝已下架，不能擦亮"]}`, `{"ret":["FAIL_BIZ_ITEM_STATUS_ERROR::宝贝已下架，不能擦亮"]}`, false, GoofishErrRejected, 1, 1},
		{"商品不存在", `{"ret":["FAIL_BIZ_ITEM_NOT_EXIST::宝贝不存在"]}`, `{"ret":["FAIL_BIZ_ITEM_NOT_EXIST::宝贝不存在"]}`, false, GoofishErrNotFound, 1, 1},
		{"主接口不明备用拒绝", `{"ret":["FAIL_SYS_SERVICE_TIMEOUT::超时"]}`, `{"ret":["FAIL_BIZ_X::失败"]}`, false, GoofishErrUncertain, 1, 1},
		{"两个都不明", `{"ret":["FAIL_SYS_SERVICE_TIMEOUT::超时"]}`, `{"ret":["FAIL_SYS_SERVICE_TIMEOUT::超时"]}`, false, GoofishErrUncertain, 1, 1},
		{"风控不走备用", `{"ret":["RGV587_ERROR::SM::哎哟喂,被挤爆啦"]}`, "", false, GoofishErrRisk, 1, 0},
		{"登录失效不走备用", `{"ret":["FAIL_SYS_SESSION_EXPIRED::Session过期"]}`, "", false, GoofishErrSessionExpired, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newGoofishStub()
			stub.handlers[GoofishPolishAPI] = func(_ int, _ map[string]any, req *http.Request) (*http.Response, error) {
				return goofishJSON(req, 200, tc.primary), nil
			}
			stub.handlers[GoofishPolishBackupAPI] = func(_ int, _ map[string]any, req *http.Request) (*http.Response, error) {
				return goofishJSON(req, 200, tc.backup), nil
			}
			already, err := goofishClient(stub).GoofishPolish(context.Background(), goofishTestCookies, "42", nil)
			if tc.want == "" {
				if err != nil || already != tc.wantAlready {
					t.Fatalf("already=%v err=%v", already, err)
				}
			} else if got := goofishKind(t, err); got != tc.want {
				t.Fatalf("kind = %s want %s (%v)", got, tc.want, err)
			}
			if stub.counts[GoofishPolishAPI] != tc.wantPrimary || stub.counts[GoofishPolishBackupAPI] != tc.wantBackup {
				t.Fatalf("调用次数 primary=%d backup=%d", stub.counts[GoofishPolishAPI], stub.counts[GoofishPolishBackupAPI])
			}
			if got := stub.last(GoofishPolishAPI); got["itemId"] != "42" || len(got) != 1 {
				t.Fatalf("polish data = %#v", got)
			}
		})
	}
}

func TestGoofishPolishPaceErrorStopsBeforeSending(t *testing.T) {
	stub := newGoofishStub()
	_, err := goofishClient(stub).GoofishPolish(context.Background(), goofishTestCookies, "42", func(context.Context) error { return context.DeadlineExceeded })
	if got := goofishKind(t, err); got != GoofishErrRejected {
		t.Fatalf("kind = %s", got)
	}
	if len(stub.calls) != 0 {
		t.Fatal("节流失败时不能发请求")
	}
	if _, err := goofishClient(stub).GoofishPolish(context.Background(), goofishTestCookies, " ", nil); goofishKind(t, err) != GoofishErrInvalid {
		t.Fatalf("空 item_id err = %v", err)
	}
}
