package server

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	goofishapp "xianyu-go/internal/application/goofishitems"
)

// goofishStubPort 是货源台商品端口替身。
type goofishStubPort struct {
	err         error
	lastPublish goofishapp.PublishInput
	lastEdit    goofishapp.EditInput
	calls       int
}

func (p *goofishStubPort) EditDetail(_ context.Context, _ int64, _, itemID string) (map[string]any, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	return map[string]any{"itemId": itemID, "itemCatDTO": map[string]any{"catId": "1"}}, nil
}

func (p *goofishStubPort) Publish(_ context.Context, input goofishapp.PublishInput) (goofishapp.WriteResult, error) {
	p.calls++
	p.lastPublish = input
	if p.err != nil {
		return goofishapp.WriteResult{}, p.err
	}
	return goofishapp.WriteResult{ItemID: "777", ItemURL: "https://www.goofish.com/item?id=777"}, nil
}

func (p *goofishStubPort) Edit(_ context.Context, input goofishapp.EditInput) (goofishapp.WriteResult, error) {
	p.calls++
	p.lastEdit = input
	if p.err != nil {
		return goofishapp.WriteResult{}, p.err
	}
	return goofishapp.WriteResult{ItemID: input.ItemID, Warnings: []string{"本地同步失败"}}, nil
}

func (p *goofishStubPort) Downshelf(_ context.Context, _ int64, _, itemID string) (goofishapp.WriteResult, error) {
	p.calls++
	if p.err != nil {
		return goofishapp.WriteResult{}, p.err
	}
	return goofishapp.WriteResult{ItemID: itemID}, nil
}

func (p *goofishStubPort) OnSale(context.Context, int64, string, int) (goofishapp.OnSalePage, error) {
	p.calls++
	if p.err != nil {
		return goofishapp.OnSalePage{}, p.err
	}
	return goofishapp.OnSalePage{Items: []goofishapp.OnSaleItem{{ItemID: "1", Title: "a", Price: "9.9", Status: "on_sale"}}, HasMore: true}, nil
}

// goofishMultipart 构造 multipart 请求体。
func goofishMultipart(t *testing.T, fields map[string]string, images int) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = writer.WriteField(k, v)
	}
	for i := 0; i < images; i++ {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="images"; filename="a.png"`)
		header.Set("Content-Type", "image/png")
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte("\x89PNG\r\n\x1a\nfake"))
	}
	_ = writer.Close()
	return &body, writer.FormDataContentType()
}

// TestGoofishItemRoutesSuccessContracts 覆盖五个货源台接口的成功响应契约。
func TestGoofishItemRoutesSuccessContracts(t *testing.T) {
	server, _, cleanup := newTestServer(t)
	defer cleanup()
	port := &goofishStubPort{}
	server.applications.goofishItems = port
	handler := server.Router()
	session := loginHelper(t, handler)

	do := func(request *http.Request) *httptest.ResponseRecorder {
		request.AddCookie(session)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		assertOpenAPISuccessResponse(t, request, recorder)
		return recorder
	}

	body, contentType := goofishMultipart(t, map[string]string{"cookie_id": "acc1", "template_item_id": "100", "title": "标题", "description": "正文", "price": "88.5"}, 2)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/goofish/items/publish", body)
	request.Header.Set("Content-Type", contentType)
	recorder := do(request)
	var published map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &published)
	if published["item_id"] != "777" || published["item_url"] != "https://www.goofish.com/item?id=777" {
		t.Fatalf("publish body = %s", recorder.Body.String())
	}
	if port.lastPublish.PriceCents != 8850 || len(port.lastPublish.Images) != 2 || port.lastPublish.TemplateItemID != "100" || port.lastPublish.UserID == 0 {
		t.Fatalf("publish input = %+v", port.lastPublish)
	}

	body, contentType = goofishMultipart(t, map[string]string{"price": "10"}, 0)
	request = httptest.NewRequest(http.MethodPost, "/api/v1/goofish/items/acc1/555/edit", body)
	request.Header.Set("Content-Type", contentType)
	recorder = do(request)
	if port.lastEdit.ItemID != "555" || port.lastEdit.PriceCents == nil || *port.lastEdit.PriceCents != 1000 || port.lastEdit.Title != nil || port.lastEdit.Description != nil {
		t.Fatalf("edit input = %+v", port.lastEdit)
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte("warnings")) {
		t.Fatalf("edit body = %s", recorder.Body.String())
	}

	recorder = do(httptest.NewRequest(http.MethodPost, "/api/v1/goofish/items/acc1/555/downshelf", nil))
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"status":"off_shelf"`)) {
		t.Fatalf("downshelf body = %s", recorder.Body.String())
	}
	recorder = do(httptest.NewRequest(http.MethodGet, "/api/v1/goofish/items/acc1/on-sale?page=2", nil))
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"has_more":true`)) {
		t.Fatalf("on-sale body = %s", recorder.Body.String())
	}
	recorder = do(httptest.NewRequest(http.MethodGet, "/api/v1/goofish/items/acc1/edit-detail?item_id=555", nil))
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"itemId":"555"`)) {
		t.Fatalf("edit-detail body = %s", recorder.Body.String())
	}
}

// TestGoofishItemRoutesErrorConvention 验证错误约定：4xx 没做成、409 风控、502 *_failed / *_uncertain。
func TestGoofishItemRoutesErrorConvention(t *testing.T) {
	server, _, cleanup := newTestServer(t)
	defer cleanup()
	handler := server.Router()
	session := loginHelper(t, handler)
	cases := []struct {
		name       string
		kind       goofishapp.ErrorKind
		path       string
		wantStatus int
		wantCode   string
	}{
		{"downshelf rejected", goofishapp.KindRejected, "/api/v1/goofish/items/acc1/9/downshelf", 502, "downshelf_failed"},
		{"downshelf uncertain", goofishapp.KindUncertain, "/api/v1/goofish/items/acc1/9/downshelf", 502, "downshelf_uncertain"},
		{"downshelf already off", goofishapp.KindAlreadyOffShelf, "/api/v1/goofish/items/acc1/9/downshelf", 409, "already_off_shelf"},
		{"downshelf missing", goofishapp.KindNotFound, "/api/v1/goofish/items/acc1/9/downshelf", 404, "item_not_found"},
		{"risk", goofishapp.KindRisk, "/api/v1/goofish/items/acc1/9/downshelf", 409, "risk_control"},
		{"offline", goofishapp.KindAccountOffline, "/api/v1/goofish/items/acc1/9/downshelf", 409, "account_offline"},
		{"invalid", goofishapp.KindInvalid, "/api/v1/goofish/items/acc1/9/downshelf", 400, "invalid_request"},
		{"on-sale failed", goofishapp.KindRejected, "/api/v1/goofish/items/acc1/on-sale", 502, "on_sale_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server.applications.goofishItems = &goofishStubPort{err: &goofishapp.Error{Kind: tc.kind, Message: "闲鱼原文"}}
			method := http.MethodPost
			if tc.path == "/api/v1/goofish/items/acc1/on-sale" {
				method = http.MethodGet
			}
			request := httptest.NewRequest(method, tc.path, nil)
			request.AddCookie(session)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			var payload struct{ Code, Message string }
			_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
			if recorder.Code != tc.wantStatus || payload.Code != tc.wantCode || payload.Message != "闲鱼原文" {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			assertOpenAPIResponse(t, request, recorder)
		})
	}

	// 参数错误、账号不属于当前用户、接口未装配都不应调用端口。
	port := &goofishStubPort{}
	server.applications.goofishItems = port
	body, contentType := goofishMultipart(t, map[string]string{"cookie_id": "acc1", "template_item_id": "1", "title": "t", "price": "0"}, 1)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/goofish/items/publish", body)
	request.Header.Set("Content-Type", contentType)
	request.AddCookie(session)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("price=0 status=%d", recorder.Code)
	}
	body, contentType = goofishMultipart(t, map[string]string{}, 0)
	request = httptest.NewRequest(http.MethodPost, "/api/v1/goofish/items/acc1/9/edit", body)
	request.Header.Set("Content-Type", contentType)
	request.AddCookie(session)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("empty edit status=%d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/goofish/items/not-mine/9/downshelf", nil)
	request.AddCookie(session)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("not owned status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if port.calls != 0 {
		t.Fatalf("端口被调用 %d 次", port.calls)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/goofish/items/acc1/9/downshelf", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("未登录 status=%d", recorder.Code)
	}
}
