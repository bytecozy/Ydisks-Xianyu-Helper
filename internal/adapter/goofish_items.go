package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	goofishapp "xianyu-go/internal/application/goofishitems"
	"xianyu-go/internal/db"
	"xianyu-go/internal/xianyu/mtop"
)

// goofishInteractiveGap 是同一账号两次闲鱼请求之间的最小间隔（交互级）。
const goofishInteractiveGap = 2 * time.Second

// goofishOperationTimeout 让整条链路在 HTTP WriteTimeout（2 分钟）之前结束，保证调用方拿到明确结果。
const goofishOperationTimeout = 100 * time.Second

// goofishItemClient 是货源台接口需要的 MTOP 能力；*mtop.ClientImpl 实现它，测试可注入替身。
type goofishItemClient interface {
	GoofishEditDetail(ctx context.Context, cookiesStr, itemID string, pace mtop.GoofishPaceFunc) (map[string]any, string, error)
	GoofishPublishFromTemplate(ctx context.Context, cookiesStr string, req mtop.GoofishPublishRequest) (*mtop.GoofishWriteResult, error)
	GoofishEditItem(ctx context.Context, cookiesStr string, req mtop.GoofishEditRequest) (*mtop.GoofishWriteResult, error)
	GoofishDownshelf(ctx context.Context, cookiesStr, itemID string, pace mtop.GoofishPaceFunc) error
	GoofishPolish(ctx context.Context, cookiesStr, itemID string, pace mtop.GoofishPaceFunc) (bool, error)
	GoofishOnSalePage(ctx context.Context, cookiesStr string, pageNumber int, pace mtop.GoofishPaceFunc) (*mtop.GoofishOnSalePage, error)
}

// GoofishItemsPort 把货源台商品接口适配到 MTOP 客户端、账号凭证和本地 item_info。
type GoofishItemsPort struct {
	// publish 复用上游单商品发布端口的存储、客户端、Cookie 写回和会话恢复。
	publish *ItemPublishPort
	// pacer 按账号节流。
	pacer *goofishPacer
}

// NewGoofishItemsPort 基于已装配的商品发布端口创建货源台接口端口。
func NewGoofishItemsPort(publish *ItemPublishPort) *GoofishItemsPort {
	return &GoofishItemsPort{publish: publish, pacer: newGoofishPacer(goofishInteractiveGap)}
}

// goofishPacer 是按账号的最小间隔节流器：每次请求预约一个时间槽，同账号串行排队。
type goofishPacer struct {
	mu   sync.Mutex
	gap  time.Duration
	next map[string]time.Time
}

// newGoofishPacer 创建节流器。
func newGoofishPacer(gap time.Duration) *goofishPacer {
	return &goofishPacer{gap: gap, next: map[string]time.Time{}}
}

// forAccount 返回某账号的节流函数。
func (p *goofishPacer) forAccount(cookieID string) mtop.GoofishPaceFunc {
	return func(ctx context.Context) error {
		p.mu.Lock()
		now := time.Now()
		slot := p.next[cookieID]
		if slot.Before(now) {
			slot = now
		}
		p.next[cookieID] = slot.Add(p.gap)
		p.mu.Unlock()
		wait := slot.Sub(now)
		if wait <= 0 {
			return ctx.Err()
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
}

// client 返回支持货源台接口的 MTOP 客户端。
func (g *GoofishItemsPort) client() (goofishItemClient, error) {
	client, ok := g.publish.mtopClient().(goofishItemClient)
	if !ok {
		return nil, errors.New("当前 MTOP 客户端不支持货源台商品接口")
	}
	return client, nil
}

// withAccount 读取账号凭证、执行闲鱼调用、写回响应 Cookie，并在登录失效时通知账号恢复。
// 返回的 warnings 记录闲鱼调用之后的本地问题（如 Cookie 写回失败），不影响调用结果。
func (g *GoofishItemsPort) withAccount(ctx context.Context, op string, userID int64, cookieID string, call func(context.Context, string, mtop.GoofishPaceFunc) error) ([]string, error) {
	if g == nil || g.publish == nil || g.publish.store == nil || g.publish.store.Cookies == nil {
		return nil, &goofishapp.Error{Op: op, Kind: goofishapp.KindRejected, Message: "货源台商品接口未初始化"}
	}
	store := g.publish.store
	unlock := store.LockAccountCredentials(cookieID)
	latest, loadErr := store.Cookies.GetCookiePlatformRuntimeData(ctx, cookieID)
	if loadErr != nil || latest.UserID != userID || !hasStoredCredential(latest) {
		unlock()
		return nil, &goofishapp.Error{Op: op, Kind: goofishapp.KindAccountOffline, Message: "账号凭证不可用，请重新登录账号后再试", Err: loadErr}
	}
	requestCtx, cancel := context.WithTimeout(ctx, goofishOperationTimeout)
	defer cancel()
	mtopCtx, session := withCookieSnapshot(requestCtx, latest)
	initialValue, initialMetadata := latest.Value, latest.MetadataJSON
	unlock()

	callErr := call(mtopCtx, latest.Value, g.pacer.forAccount(cookieID))

	var warnings []string
	if runtimeCookie, persistErr := g.persistSession(ctx, userID, cookieID, initialValue, initialMetadata, session); persistErr != nil {
		warnings = append(warnings, "闲鱼返回的新 Cookie 没有保存："+persistErr.Error())
	} else if runtimeCookie != "" && g.publish.updateRunningCookie != nil {
		g.publish.updateRunningCookie(ctx, cookieID, runtimeCookie)
	}
	if callErr != nil {
		mapped := goofishApplicationError(op, callErr)
		if mapped.Kind == goofishapp.KindAccountOffline && g.publish.recoverExpiredSession != nil {
			// 账号恢复可能较慢，放到后台，避免拖住本次 HTTP 响应。
			recoverCtx, recoverCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
			go func() {
				defer recoverCancel()
				g.publish.recoverExpiredSession(recoverCtx, cookieID, callErr)
			}()
		}
		return warnings, mapped
	}
	return warnings, nil
}

// persistSession 在远端调用后复核凭证未被他人改动，再保存 Cookie 会话变化。
func (g *GoofishItemsPort) persistSession(ctx context.Context, userID int64, cookieID, initialValue, initialMetadata string, session *mtop.CookieSession) (string, error) {
	store := g.publish.store
	unlock := store.LockAccountCredentials(cookieID)
	defer unlock()
	latest, loadErr := store.Cookies.GetCookiePlatformRuntimeData(ctx, cookieID)
	if loadErr != nil {
		return "", loadErr
	}
	if latest.UserID != userID || latest.Value != initialValue || latest.MetadataJSON != initialMetadata {
		// 期间凭证被其他流程更新（如续期），以对方为准，不覆盖。
		return "", nil
	}
	value, valueChanged, handled, err := g.publish.persistSession(ctx, latest, session)
	if err != nil {
		return "", err
	}
	if handled && valueChanged {
		return value, nil
	}
	return "", nil
}

// goofishApplicationError 把 MTOP 分类错误转成应用层错误。
func goofishApplicationError(op string, err error) *goofishapp.Error {
	var gerr *mtop.GoofishError
	if errors.As(err, &gerr) {
		kind := goofishapp.KindRejected
		switch gerr.Kind {
		case mtop.GoofishErrInvalid:
			kind = goofishapp.KindInvalid
		case mtop.GoofishErrNotFound:
			kind = goofishapp.KindNotFound
		case mtop.GoofishErrAlreadyOffShelf:
			kind = goofishapp.KindAlreadyOffShelf
		case mtop.GoofishErrSessionExpired:
			kind = goofishapp.KindAccountOffline
		case mtop.GoofishErrRisk:
			kind = goofishapp.KindRisk
		case mtop.GoofishErrUncertain:
			kind = goofishapp.KindUncertain
		}
		return &goofishapp.Error{Op: op, Kind: kind, Message: gerr.Message, Err: err}
	}
	var appErr *goofishapp.Error
	if errors.As(err, &appErr) {
		return appErr
	}
	// 未分类错误只可能来自写请求之前（参数、凭证），按没做成处理。
	return &goofishapp.Error{Op: op, Kind: goofishapp.KindRejected, Message: err.Error(), Err: err}
}

// EditDetail 原样返回 editDetail 的 data。
func (g *GoofishItemsPort) EditDetail(ctx context.Context, userID int64, cookieID, itemID string) (map[string]any, error) {
	client, err := g.client()
	if err != nil {
		return nil, &goofishapp.Error{Op: "edit_detail", Kind: goofishapp.KindRejected, Message: err.Error()}
	}
	var data map[string]any
	_, err = g.withAccount(ctx, "edit_detail", userID, cookieID, func(ctx context.Context, cookies string, pace mtop.GoofishPaceFunc) error {
		var callErr error
		data, _, callErr = client.GoofishEditDetail(ctx, cookies, itemID, pace)
		return callErr
	})
	return data, err
}

// Publish 按模板发布并写入本地 item_info。
func (g *GoofishItemsPort) Publish(ctx context.Context, input goofishapp.PublishInput) (goofishapp.WriteResult, error) {
	client, err := g.client()
	if err != nil {
		return goofishapp.WriteResult{}, &goofishapp.Error{Op: "publish", Kind: goofishapp.KindRejected, Message: err.Error()}
	}
	var result *mtop.GoofishWriteResult
	warnings, err := g.withAccount(ctx, "publish", input.UserID, input.CookieID, func(ctx context.Context, cookies string, pace mtop.GoofishPaceFunc) error {
		var callErr error
		result, callErr = client.GoofishPublishFromTemplate(ctx, cookies, mtop.GoofishPublishRequest{
			TemplateItemID: input.TemplateItemID, Title: input.Title, Description: input.Description,
			PriceCents: input.PriceCents, Images: goofishImages(input.Images), Pace: pace,
		})
		return callErr
	})
	if err != nil {
		return goofishapp.WriteResult{}, err
	}
	if saveErr := g.saveItem(ctx, input.CookieID, result, map[string]any{"goofish_template_item_id": input.TemplateItemID}); saveErr != nil {
		warnings = append(warnings, "闲鱼已发布，但本地 item_info 保存失败："+saveErr.Error())
	}
	return goofishapp.WriteResult{ItemID: result.ItemID, ItemURL: result.ItemURL, Warnings: warnings}, nil
}

// Edit 编辑商品并同步本地 item_info。
func (g *GoofishItemsPort) Edit(ctx context.Context, input goofishapp.EditInput) (goofishapp.WriteResult, error) {
	client, err := g.client()
	if err != nil {
		return goofishapp.WriteResult{}, &goofishapp.Error{Op: "edit", Kind: goofishapp.KindRejected, Message: err.Error()}
	}
	var result *mtop.GoofishWriteResult
	warnings, err := g.withAccount(ctx, "edit", input.UserID, input.CookieID, func(ctx context.Context, cookies string, pace mtop.GoofishPaceFunc) error {
		var callErr error
		result, callErr = client.GoofishEditItem(ctx, cookies, mtop.GoofishEditRequest{
			ItemID: input.ItemID, Title: input.Title, Description: input.Description,
			PriceCents: input.PriceCents, Images: goofishImages(input.Images), Pace: pace,
		})
		return callErr
	})
	if err != nil {
		return goofishapp.WriteResult{}, err
	}
	if saveErr := g.saveItem(ctx, input.CookieID, result, map[string]any{"goofish_edited_at": time.Now().Unix()}); saveErr != nil {
		warnings = append(warnings, "闲鱼已修改，但本地 item_info 同步失败："+saveErr.Error())
	}
	return goofishapp.WriteResult{ItemID: input.ItemID, ItemURL: result.ItemURL, Warnings: warnings}, nil
}

// Downshelf 下架商品并在本地 item_info 详情里记下状态。
func (g *GoofishItemsPort) Downshelf(ctx context.Context, userID int64, cookieID, itemID string) (goofishapp.WriteResult, error) {
	client, err := g.client()
	if err != nil {
		return goofishapp.WriteResult{}, &goofishapp.Error{Op: "downshelf", Kind: goofishapp.KindRejected, Message: err.Error()}
	}
	warnings, err := g.withAccount(ctx, "downshelf", userID, cookieID, func(ctx context.Context, cookies string, pace mtop.GoofishPaceFunc) error {
		return client.GoofishDownshelf(ctx, cookies, itemID, pace)
	})
	var appErr *goofishapp.Error
	if err != nil && !(errors.As(err, &appErr) && appErr.Kind == goofishapp.KindAlreadyOffShelf) {
		return goofishapp.WriteResult{}, err
	}
	if markErr := g.markOffShelf(ctx, cookieID, itemID); markErr != nil {
		warnings = append(warnings, "本地 item_info 状态没有更新："+markErr.Error())
	}
	return goofishapp.WriteResult{ItemID: itemID, Warnings: warnings}, err
}

// Polish 擦亮商品（今天已擦亮也算成功），不写本地库。
func (g *GoofishItemsPort) Polish(ctx context.Context, userID int64, cookieID, itemID string) (goofishapp.PolishResult, error) {
	client, err := g.client()
	if err != nil {
		return goofishapp.PolishResult{}, &goofishapp.Error{Op: "polish", Kind: goofishapp.KindRejected, Message: err.Error()}
	}
	already := false
	warnings, err := g.withAccount(ctx, "polish", userID, cookieID, func(ctx context.Context, cookies string, pace mtop.GoofishPaceFunc) error {
		var callErr error
		already, callErr = client.GoofishPolish(ctx, cookies, itemID, pace)
		return callErr
	})
	if err != nil {
		return goofishapp.PolishResult{}, err
	}
	return goofishapp.PolishResult{ItemID: itemID, AlreadyPolished: already, Warnings: warnings}, nil
}

// OnSale 读取一页在售商品，不写本地库。
func (g *GoofishItemsPort) OnSale(ctx context.Context, userID int64, cookieID string, page int) (goofishapp.OnSalePage, error) {
	client, err := g.client()
	if err != nil {
		return goofishapp.OnSalePage{}, &goofishapp.Error{Op: "on_sale", Kind: goofishapp.KindRejected, Message: err.Error()}
	}
	var result *mtop.GoofishOnSalePage
	_, err = g.withAccount(ctx, "on_sale", userID, cookieID, func(ctx context.Context, cookies string, pace mtop.GoofishPaceFunc) error {
		var callErr error
		result, callErr = client.GoofishOnSalePage(ctx, cookies, page, pace)
		return callErr
	})
	if err != nil {
		return goofishapp.OnSalePage{}, err
	}
	out := goofishapp.OnSalePage{Items: make([]goofishapp.OnSaleItem, 0, len(result.Items)), HasMore: result.HasMore}
	for _, item := range result.Items {
		out.Items = append(out.Items, goofishapp.OnSaleItem{ItemID: item.ItemID, Title: item.Title, Price: item.Price, Status: item.Status})
	}
	return out, nil
}

// saveItem 把发布/编辑后的商品写入 item_info：保留多规格/多数量开关，详情 JSON 合并已有字段。
func (g *GoofishItemsPort) saveItem(ctx context.Context, cookieID string, result *mtop.GoofishWriteResult, extra map[string]any) error {
	if result == nil || g.publish.store.Items == nil {
		return errors.New("商品存储未初始化")
	}
	items := g.publish.store.Items
	detail := map[string]any{}
	existing, getErr := items.GetByCookieItem(ctx, cookieID, result.ItemID)
	if getErr != nil && !errors.Is(getErr, db.ErrNotFound) {
		return getErr
	}
	if getErr == nil && strings.TrimSpace(existing.ItemDetail) != "" {
		_ = json.Unmarshal([]byte(existing.ItemDetail), &detail)
	}
	if result.ImageURL != "" {
		detail["item_image"] = result.ImageURL
	}
	detail["web_url"] = result.ItemURL
	if result.CategoryName != "" {
		detail["category_name"] = result.CategoryName
	}
	detail["quantity"] = result.Quantity
	detail["goofish_status"] = "on_sale"
	for k, v := range extra {
		detail[k] = v
	}
	detailJSON, _ := json.Marshal(detail)
	price := ""
	if result.PriceCents > 0 {
		price = strconv.FormatFloat(float64(result.PriceCents)/100, 'f', 2, 64)
	}
	row := &db.ItemInfoRow{
		CookieID: cookieID, ItemID: result.ItemID, ItemTitle: result.Title, ItemDescription: result.Description,
		ItemCategory: result.CategoryID, ItemPrice: price, ItemDetail: string(detailJSON),
	}
	if getErr != nil {
		row.MultiQuantityDelivery = result.Quantity > 1
		return items.Upsert(ctx, row)
	}
	return items.UpsertBasic(ctx, row)
}

// markOffShelf 在本地商品详情里记下已下架；本地没有该商品时不新建。
func (g *GoofishItemsPort) markOffShelf(ctx context.Context, cookieID, itemID string) error {
	items := g.publish.store.Items
	if items == nil {
		return errors.New("商品存储未初始化")
	}
	existing, err := items.GetByCookieItem(ctx, cookieID, itemID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil
		}
		return err
	}
	detail := map[string]any{}
	if strings.TrimSpace(existing.ItemDetail) != "" {
		_ = json.Unmarshal([]byte(existing.ItemDetail), &detail)
	}
	detail["goofish_status"] = "off_shelf"
	detail["goofish_off_shelf_at"] = time.Now().Unix()
	detailJSON, _ := json.Marshal(detail)
	value := string(detailJSON)
	return items.Patch(ctx, cookieID, itemID, db.ItemPatch{ItemDetail: &value})
}

// goofishImages 转换图片模型。
func goofishImages(images []goofishapp.Image) []mtop.PublishImage {
	out := make([]mtop.PublishImage, 0, len(images))
	for _, img := range images {
		out = append(out, mtop.PublishImage{Filename: img.Filename, ContentType: img.ContentType, Data: img.Data})
	}
	return out
}

var _ goofishapp.Port = (*GoofishItemsPort)(nil)
