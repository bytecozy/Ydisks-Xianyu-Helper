package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	goofishapp "xianyu-go/internal/application/goofishitems"
	"xianyu-go/internal/xianyu/mtop"
)

// goofishClientStub 是货源台 MTOP 能力替身。
type goofishClientStub struct {
	mtop.Client
	publishErr   error
	downshelfErr error
	polishErr    error
	polishedOnce bool
	polishPaced  bool
	sawCookies   string
}

func (c *goofishClientStub) GoofishEditDetail(_ context.Context, cookies, itemID string, pace mtop.GoofishPaceFunc) (map[string]any, string, error) {
	c.sawCookies = cookies
	return map[string]any{"itemId": itemID}, "", pace(context.Background())
}

func (c *goofishClientStub) GoofishPublishFromTemplate(_ context.Context, cookies string, req mtop.GoofishPublishRequest) (*mtop.GoofishWriteResult, error) {
	c.sawCookies = cookies
	if c.publishErr != nil {
		return nil, c.publishErr
	}
	return &mtop.GoofishWriteResult{ItemID: "777", ItemURL: "https://www.goofish.com/item?id=777", Title: req.Title, Description: req.Title + "\n\n" + req.Description, PriceCents: req.PriceCents, Quantity: 1, CategoryID: "50025461", CategoryName: "游戏账号", ImageURL: "https://img/1.jpg"}, nil
}

func (c *goofishClientStub) GoofishEditItem(_ context.Context, _ string, req mtop.GoofishEditRequest) (*mtop.GoofishWriteResult, error) {
	return &mtop.GoofishWriteResult{ItemID: req.ItemID, ItemURL: "u", Title: "新标题", Description: "新标题\n\n正文", PriceCents: 9900, Quantity: 1, CategoryID: "50025461"}, nil
}

func (c *goofishClientStub) GoofishDownshelf(context.Context, string, string, mtop.GoofishPaceFunc) error {
	return c.downshelfErr
}

func (c *goofishClientStub) GoofishPolish(_ context.Context, cookies, _ string, pace mtop.GoofishPaceFunc) (bool, error) {
	c.sawCookies = cookies
	c.polishPaced = pace != nil && pace(context.Background()) == nil
	return c.polishedOnce, c.polishErr
}

func (c *goofishClientStub) GoofishOnSalePage(context.Context, string, int, mtop.GoofishPaceFunc) (*mtop.GoofishOnSalePage, error) {
	return &mtop.GoofishOnSalePage{Items: []mtop.GoofishOnSaleItem{{ItemID: "1", Title: "a", Price: "1", Status: "on_sale"}}, HasMore: false}, nil
}

func newGoofishTestPort(t *testing.T, client *goofishClientStub) (*GoofishItemsPort, func()) {
	t.Helper()
	store, cleanup := newAdapterTestStore(t)
	publish := NewItemPublishPort(store, func() mtop.Client { return client }, nil, nil, nil)
	port := NewGoofishItemsPort(publish)
	port.pacer = newGoofishPacer(0)
	return port, cleanup
}

func TestGoofishItemsPortPublishEditDownshelfSyncItemInfo(t *testing.T) {
	client := &goofishClientStub{}
	port, cleanup := newGoofishTestPort(t, client)
	defer cleanup()
	ctx := context.Background()
	result, err := port.Publish(ctx, goofishapp.PublishInput{UserID: 1, CookieID: "cid", TemplateItemID: "100", Title: "标题", Description: "正文", PriceCents: 8850})
	if err != nil || result.ItemID != "777" || len(result.Warnings) != 0 {
		t.Fatalf("publish = %+v err=%v", result, err)
	}
	if client.sawCookies == "" {
		t.Fatal("没有把账号 Cookie 交给 MTOP")
	}
	row, err := port.publish.store.Items.GetByCookieItem(ctx, "cid", "777")
	if err != nil || row.ItemTitle != "标题" || row.ItemPrice != "88.50" || row.ItemDescription != "标题\n\n正文" || row.ItemCategory != "50025461" {
		t.Fatalf("item_info = %+v err=%v", row, err)
	}
	var detail map[string]any
	_ = json.Unmarshal([]byte(row.ItemDetail), &detail)
	if detail["goofish_status"] != "on_sale" || detail["goofish_template_item_id"] != "100" || detail["item_image"] != "https://img/1.jpg" {
		t.Fatalf("detail = %#v", detail)
	}
	if err := port.publish.store.Items.SetMultiSpec(ctx, "cid", "777", true); err != nil {
		t.Fatal(err)
	}

	price := int64(9900)
	if _, err := port.Edit(ctx, goofishapp.EditInput{UserID: 1, CookieID: "cid", ItemID: "777", PriceCents: &price}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	row, _ = port.publish.store.Items.GetByCookieItem(ctx, "cid", "777")
	_ = json.Unmarshal([]byte(row.ItemDetail), &detail)
	if row.ItemTitle != "新标题" || row.ItemPrice != "99.00" || !row.IsMultiSpec || detail["goofish_template_item_id"] != "100" {
		t.Fatalf("edit 后 item_info = %+v detail=%#v", row, detail)
	}

	if _, err := port.Downshelf(ctx, 1, "cid", "777"); err != nil {
		t.Fatalf("downshelf: %v", err)
	}
	row, _ = port.publish.store.Items.GetByCookieItem(ctx, "cid", "777")
	_ = json.Unmarshal([]byte(row.ItemDetail), &detail)
	if detail["goofish_status"] != "off_shelf" {
		t.Fatalf("downshelf 后 detail = %#v", detail)
	}
}

func TestGoofishItemsPortErrorMapping(t *testing.T) {
	cases := map[mtop.GoofishErrorKind]goofishapp.ErrorKind{
		mtop.GoofishErrInvalid:         goofishapp.KindInvalid,
		mtop.GoofishErrNotFound:        goofishapp.KindNotFound,
		mtop.GoofishErrSessionExpired:  goofishapp.KindAccountOffline,
		mtop.GoofishErrRisk:            goofishapp.KindRisk,
		mtop.GoofishErrRejected:        goofishapp.KindRejected,
		mtop.GoofishErrUncertain:       goofishapp.KindUncertain,
		mtop.GoofishErrAlreadyOffShelf: goofishapp.KindAlreadyOffShelf,
	}
	for from, want := range cases {
		client := &goofishClientStub{publishErr: &mtop.GoofishError{Kind: from, Message: "原文"}}
		port, cleanup := newGoofishTestPort(t, client)
		_, err := port.Publish(context.Background(), goofishapp.PublishInput{UserID: 1, CookieID: "cid", TemplateItemID: "1", Title: "t", PriceCents: 1})
		var appErr *goofishapp.Error
		if !errors.As(err, &appErr) || appErr.Kind != want || appErr.Op != "publish" || appErr.Message != "原文" {
			t.Fatalf("%s → %#v", from, err)
		}
		if _, getErr := port.publish.store.Items.GetByCookieItem(context.Background(), "cid", "777"); getErr == nil {
			t.Fatalf("%s: 失败时不能写 item_info", from)
		}
		cleanup()
	}
}

func TestGoofishItemsPortRejectsForeignAccount(t *testing.T) {
	port, cleanup := newGoofishTestPort(t, &goofishClientStub{})
	defer cleanup()
	_, err := port.Downshelf(context.Background(), 99, "cid", "1")
	var appErr *goofishapp.Error
	if !errors.As(err, &appErr) || appErr.Kind != goofishapp.KindAccountOffline {
		t.Fatalf("err = %#v", err)
	}
}

func TestGoofishItemsPortAlreadyOffShelfStillMarksLocal(t *testing.T) {
	client := &goofishClientStub{downshelfErr: &mtop.GoofishError{Kind: mtop.GoofishErrAlreadyOffShelf, Message: "已下架"}}
	port, cleanup := newGoofishTestPort(t, client)
	defer cleanup()
	if _, err := port.Publish(context.Background(), goofishapp.PublishInput{UserID: 1, CookieID: "cid", TemplateItemID: "1", Title: "t", PriceCents: 1}); err != nil {
		t.Fatal(err)
	}
	_, err := port.Downshelf(context.Background(), 1, "cid", "777")
	var appErr *goofishapp.Error
	if !errors.As(err, &appErr) || appErr.Kind != goofishapp.KindAlreadyOffShelf {
		t.Fatalf("err = %#v", err)
	}
	row, _ := port.publish.store.Items.GetByCookieItem(context.Background(), "cid", "777")
	var detail map[string]any
	_ = json.Unmarshal([]byte(row.ItemDetail), &detail)
	if detail["goofish_status"] != "off_shelf" {
		t.Fatalf("detail = %#v", detail)
	}
}

func TestGoofishPacerSpacesSameAccount(t *testing.T) {
	pacer := newGoofishPacer(50 * time.Millisecond)
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := pacer.forAccount("a")(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("同账号 3 次请求只用了 %s，应至少间隔 2×50ms", elapsed)
	}
	start = time.Now()
	if err := pacer.forAccount("b")(ctx); err != nil || time.Since(start) > 20*time.Millisecond {
		t.Fatalf("不同账号不应互相等待: %v %s", err, time.Since(start))
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := pacer.forAccount("a")(cancelled); err == nil {
		t.Fatal("取消的上下文应返回错误")
	}
}

func TestGoofishItemsPortPolish(t *testing.T) {
	client := &goofishClientStub{}
	port, cleanup := newGoofishTestPort(t, client)
	defer cleanup()
	ctx := context.Background()
	result, err := port.Polish(ctx, 1, "cid", "777")
	if err != nil || result.ItemID != "777" || result.AlreadyPolished || len(result.Warnings) != 0 {
		t.Fatalf("polish = %+v err=%v", result, err)
	}
	if client.sawCookies == "" || !client.polishPaced {
		t.Fatalf("没有把账号 Cookie 和节流器交给 MTOP: cookies=%q paced=%v", client.sawCookies, client.polishPaced)
	}
	client.polishedOnce = true
	result, err = port.Polish(ctx, 1, "cid", "777")
	if err != nil || !result.AlreadyPolished {
		t.Fatalf("already polished = %+v err=%v", result, err)
	}
	if _, getErr := port.publish.store.Items.GetByCookieItem(ctx, "cid", "777"); getErr == nil {
		t.Fatal("擦亮不应写 item_info")
	}

	client.polishedOnce = false
	cases := map[mtop.GoofishErrorKind]goofishapp.ErrorKind{
		mtop.GoofishErrNotFound:       goofishapp.KindNotFound,
		mtop.GoofishErrSessionExpired: goofishapp.KindAccountOffline,
		mtop.GoofishErrRisk:           goofishapp.KindRisk,
		mtop.GoofishErrRejected:       goofishapp.KindRejected,
		mtop.GoofishErrUncertain:      goofishapp.KindUncertain,
	}
	for from, want := range cases {
		client.polishErr = &mtop.GoofishError{Kind: from, Message: "原文"}
		_, err := port.Polish(ctx, 1, "cid", "777")
		var appErr *goofishapp.Error
		if !errors.As(err, &appErr) || appErr.Kind != want || appErr.Op != "polish" || appErr.Message != "原文" {
			t.Fatalf("%s → %#v", from, err)
		}
	}
	if _, err := port.Polish(ctx, 99, "cid", "777"); err == nil {
		t.Fatal("别人的账号应该失败")
	}
}
