// Package mtop: 货源台（goofish fork）专用的闲鱼商品接口——按模板发布、编辑、下架、在售列表和 editDetail。
//
// 本文件只在 bytecozy/Ydisks-Xianyu-Helper 的 goofish 分支存在，刻意不改动上游发布实现，
// 方便以后 rebase 上游。所有调用都经过 goofishCall：按账号节流、Token 过期重签、风控识别不重试，
// 并把结果分成「肯定没做成」和「请求已发出但结果不明」两类，供货源台决定是否需要人工确认。
package mtop

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"xianyu-go/internal/xianyu/protocol"
)

// 货源台接口使用的 MTOP API 名称与版本。
const (
	GoofishEditDetailAPI = "mtop.idle.pc.idleitem.editDetail"
	GoofishEditAPI       = "mtop.idle.pc.idleitem.edit"
	GoofishPublishAPI    = "mtop.idle.pc.idleitem.publish"
	GoofishDownshelfAPI  = "mtop.taobao.idle.item.downshelf"
	GoofishPolishAPI     = "mtop.taobao.idle.item.polish"
	// GoofishPolishBackupAPI 是擦亮主接口失败时的备用接口（与上游 PolishItem 相同）。
	GoofishPolishBackupAPI = "mtop.idle.item.polish"
	GoofishItemListAPI     = "mtop.idle.web.xyh.item.list"
	goofishH5Base          = "https://h5api.m.goofish.com/h5/"
	goofishSourceID        = "pcMainPublish"
	goofishMaxImages       = 9
)

// GoofishErrorKind 是货源台接口的失败分类；HTTP 层据此决定状态码和 code 后缀。
type GoofishErrorKind string

const (
	// GoofishErrInvalid 表示参数或模板不可用，写请求没有发出。
	GoofishErrInvalid GoofishErrorKind = "invalid"
	// GoofishErrNotFound 表示闲鱼明确说商品不存在（或不属于该账号）。
	GoofishErrNotFound GoofishErrorKind = "not_found"
	// GoofishErrAlreadyOffShelf 表示下架时商品本来就不在售。
	GoofishErrAlreadyOffShelf GoofishErrorKind = "already_off_shelf"
	// GoofishErrSessionExpired 表示登录 Session 失效，请求被网关拒绝。
	GoofishErrSessionExpired GoofishErrorKind = "session_expired"
	// GoofishErrRisk 表示触发风控（滑块、punish 等），请求被拦截且不重试。
	GoofishErrRisk GoofishErrorKind = "risk_control"
	// GoofishErrRejected 表示闲鱼明确拒绝，或写请求根本没有发出：肯定没有改到闲鱼。
	GoofishErrRejected GoofishErrorKind = "rejected"
	// GoofishErrUncertain 表示写请求已发出但结果不明：超时、断连、5xx、系统错误、成功却缺商品 ID。
	GoofishErrUncertain GoofishErrorKind = "uncertain"
)

// GoofishError 是货源台接口的分类错误。Message 带闲鱼原文（已脱敏），不含 Cookie。
type GoofishError struct {
	// Kind 是失败分类。
	Kind GoofishErrorKind
	// Stage 是失败发生的步骤：edit_detail、upload、publish、edit、downshelf、item_list。
	Stage string
	// Message 是可直接返回给调用方的中文说明。
	Message string
	// Ret 是闲鱼返回的 ret 副本（已脱敏）。
	Ret []string
	// Err 保留底层错误链。
	Err error
}

// Error 返回带步骤的失败说明。
func (e *GoofishError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Unwrap 返回底层错误。
func (e *GoofishError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// GoofishPaceFunc 在每次向闲鱼发请求前调用，用于按账号节流；返回错误时请求不会发出。
type GoofishPaceFunc func(context.Context) error

// GoofishPublishRequest 是按模板商品发布的新商品内容。
type GoofishPublishRequest struct {
	// TemplateItemID 是同账号下已发布的模板商品，沿用它的类目、属性、发货方式和发货地。
	TemplateItemID string
	// Title 是商品标题，同时作为描述第一行。
	Title string
	// Description 是标题之后的正文；若第一行已经是标题则原样使用。
	Description string
	// PriceCents 是售价（分）。
	PriceCents int64
	// Images 是按顺序上传的商品图片，1–9 张。
	Images []PublishImage
	// Pace 是按账号节流器。
	Pace GoofishPaceFunc
}

// GoofishEditRequest 是对已有商品的局部修改；nil/空表示不改。
type GoofishEditRequest struct {
	// ItemID 是要修改的商品。
	ItemID string
	// Title 为非 nil 时替换标题（并替换描述第一行）。
	Title *string
	// Description 为非 nil 时替换标题之后的正文。
	Description *string
	// PriceCents 为非 nil 时替换售价（分）。
	PriceCents *int64
	// Images 非空时整组按顺序替换商品图片。
	Images []PublishImage
	// Pace 是按账号节流器。
	Pace GoofishPaceFunc
}

// GoofishWriteResult 是发布或编辑成功后的商品快照，用于同步本地 item_info。
type GoofishWriteResult struct {
	ItemID       string
	ItemURL      string
	Title        string
	Description  string
	PriceCents   int64
	Quantity     int
	CategoryID   string
	CategoryName string
	ImageURL     string
	// Payload 是实际发给闲鱼的 data（不含 Cookie），仅用于排查。
	Payload map[string]any
	// RawData 是闲鱼成功响应的 data。
	RawData map[string]any
}

// GoofishOnSaleItem 是在售列表中的一行。
type GoofishOnSaleItem struct {
	ItemID string `json:"item_id"`
	Title  string `json:"title"`
	Price  string `json:"price"`
	Status string `json:"status"`
}

// GoofishOnSalePage 是在售列表的一页。
type GoofishOnSalePage struct {
	Items   []GoofishOnSaleItem
	HasMore bool
}

// goofishAPI 描述一个 MTOP 接口的调用参数。
type goofishAPI struct {
	api     string
	version string
	spmCnt  string
	spmPre  string
	stage   string
	// write 表示这是会改动闲鱼的写请求；写请求发出后的不明结果归为 uncertain。
	write bool
}

var (
	goofishEditDetailSpec   = goofishAPI{api: GoofishEditDetailAPI, version: "1.0", spmCnt: "a21ybx.publish.0.0", spmPre: "a21ybx.item.0.0", stage: "edit_detail"}
	goofishEditSpec         = goofishAPI{api: GoofishEditAPI, version: "1.0", spmCnt: "a21ybx.publish.0.0", spmPre: "a21ybx.item.0.0", stage: "edit", write: true}
	goofishPublishSpec      = goofishAPI{api: GoofishPublishAPI, version: "1.0", spmCnt: "a21ybx.publish.0.0", spmPre: "a21ybx.home.sidebar.1.46413da6EPl7v5", stage: "publish", write: true}
	goofishDownshelfSpec    = goofishAPI{api: GoofishDownshelfAPI, version: "2.0", spmCnt: "a21ybx.item.0.0", spmPre: "a21ybx.personal.feeds.1.0", stage: "downshelf", write: true}
	goofishPolishSpec       = goofishAPI{api: GoofishPolishAPI, version: "2.0", spmCnt: "a21ybx.item.0.0", spmPre: "a21ybx.personal.feeds.1.0", stage: "polish", write: true}
	goofishPolishBackupSpec = goofishAPI{api: GoofishPolishBackupAPI, version: "1.0", spmCnt: "a21ybx.item.0.0", spmPre: "a21ybx.personal.feeds.1.0", stage: "polish", write: true}
	goofishItemListSpec     = goofishAPI{api: GoofishItemListAPI, version: "1.0", spmCnt: "a21ybx.personal.0.0", spmPre: "a21ybx.home.sidebar.1.0", stage: "item_list"}
)

// GoofishEditDetail 读取 mtop.idle.pc.idleitem.editDetail 1.0 的 data。
func (c *ClientImpl) GoofishEditDetail(ctx context.Context, cookiesStr, itemID string, pace GoofishPaceFunc) (map[string]any, string, error) {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return nil, cookiesStr, &GoofishError{Kind: GoofishErrInvalid, Stage: "edit_detail", Message: "item_id 不能为空"}
	}
	decoded, updated, err := c.goofishCall(ctx, cookiesStr, pace, goofishEditDetailSpec, map[string]any{"itemId": itemID})
	if err != nil {
		return nil, updated, goofishNotFoundFromRejection(err)
	}
	data := mapFromAny(decoded["data"])
	if data == nil {
		return nil, updated, &GoofishError{Kind: GoofishErrRejected, Stage: "edit_detail", Message: "editDetail 成功但缺少 data", Ret: sanitizeMTopRet(retFromDecoded(decoded))}
	}
	return data, updated, nil
}

// GoofishPublishFromTemplate 取模板商品的 editDetail，沿用类目、属性、发货方式、发货地和数量，
// 换掉标题、描述、价格和图片后调用 mtop.idle.pc.idleitem.publish 1.0。
func (c *ClientImpl) GoofishPublishFromTemplate(ctx context.Context, cookiesStr string, req GoofishPublishRequest) (*GoofishWriteResult, error) {
	if err := validateGoofishTitle("publish", req.Title); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.TemplateItemID) == "" {
		return nil, goofishInvalid("publish", "template_item_id 不能为空")
	}
	if req.PriceCents <= 0 {
		return nil, goofishInvalid("publish", "价格必须大于 0")
	}
	if len(req.Images) == 0 {
		return nil, goofishInvalid("publish", "至少上传 1 张商品图片")
	}
	if len(req.Images) > goofishMaxImages {
		return nil, goofishInvalid("publish", "商品图片最多 9 张")
	}
	current := goofishCurrentCookies(ctx, cookiesStr)
	template, updated, err := c.GoofishEditDetail(ctx, current, req.TemplateItemID, req.Pace)
	current = goofishPick(current, updated)
	if err != nil {
		return nil, goofishTemplateError(err)
	}
	if err := goofishTemplateUsable(template); err != nil {
		return nil, err
	}
	images, updated, err := c.goofishUploadImages(ctx, current, req.Images, req.Pace)
	current = goofishPick(current, updated)
	if err != nil {
		return nil, err
	}
	data := buildGoofishPublishPayload(template, req, images)
	decoded, _, err := c.goofishCall(ctx, current, req.Pace, goofishPublishSpec, data)
	if err != nil {
		return nil, err
	}
	dataMap := mapFromAny(decoded["data"])
	itemID := findStringDeep(dataMap, "itemId", "item_id", "itemID")
	if itemID == "" {
		itemID = findStringDeep(decoded, "itemId", "item_id", "itemID")
	}
	if itemID == "" {
		return nil, &GoofishError{Kind: GoofishErrUncertain, Stage: "publish", Message: "闲鱼返回发布成功，但响应里没有商品 ID，无法确认是否发布", Ret: sanitizeMTopRet(retFromDecoded(decoded))}
	}
	cat := mapFromAny(data["itemCatDTO"])
	text := mapFromAny(data["itemTextDTO"])
	return &GoofishWriteResult{
		ItemID: itemID, ItemURL: goofishItemURL(itemID), Title: mtopString(text["title"]), Description: mtopString(text["desc"]),
		PriceCents: req.PriceCents, Quantity: goofishQuantity(data["quantity"]), CategoryID: mtopString(cat["catId"]), CategoryName: mtopString(cat["catName"]),
		ImageURL: images[0].URL, Payload: data, RawData: dataMap,
	}, nil
}

// GoofishEditItem 取 editDetail，改标题/描述/价格/图片后调用 mtop.idle.pc.idleitem.edit 1.0。
func (c *ClientImpl) GoofishEditItem(ctx context.Context, cookiesStr string, req GoofishEditRequest) (*GoofishWriteResult, error) {
	req.ItemID = strings.TrimSpace(req.ItemID)
	if req.ItemID == "" {
		return nil, goofishInvalid("edit", "item_id 不能为空")
	}
	if req.Title == nil && req.Description == nil && req.PriceCents == nil && len(req.Images) == 0 {
		return nil, goofishInvalid("edit", "没有要修改的字段")
	}
	if req.Title != nil && strings.TrimSpace(*req.Title) == "" {
		return nil, goofishInvalid("edit", "标题不能为空")
	}
	if req.PriceCents != nil && *req.PriceCents <= 0 {
		return nil, goofishInvalid("edit", "价格必须大于 0")
	}
	if len(req.Images) > goofishMaxImages {
		return nil, goofishInvalid("edit", "商品图片最多 9 张")
	}
	current := goofishCurrentCookies(ctx, cookiesStr)
	detail, updated, err := c.GoofishEditDetail(ctx, current, req.ItemID, req.Pace)
	current = goofishPick(current, updated)
	if err != nil {
		return nil, goofishStageError(err, "edit")
	}
	if req.PriceCents != nil && goofishHasSKUs(detail) {
		return nil, goofishInvalid("edit", "多规格商品不支持按单一价格修改")
	}
	var images []uploadedImage
	if len(req.Images) > 0 {
		images, updated, err = c.goofishUploadImages(ctx, current, req.Images, req.Pace)
		current = goofishPick(current, updated)
		if err != nil {
			return nil, err
		}
	}
	data, err := buildGoofishEditPayload(detail, req, images)
	if err != nil {
		return nil, err
	}
	decoded, _, err := c.goofishCall(ctx, current, req.Pace, goofishEditSpec, data)
	if err != nil {
		return nil, goofishNotFoundFromRejection(err)
	}
	cat := mapFromAny(data["itemCatDTO"])
	text := mapFromAny(data["itemTextDTO"])
	price, _ := strconv.ParseInt(mtopString(mapFromAny(data["itemPriceDTO"])["priceInCent"]), 10, 64)
	result := &GoofishWriteResult{
		ItemID: req.ItemID, ItemURL: goofishItemURL(req.ItemID), Title: mtopString(text["title"]), Description: mtopString(text["desc"]),
		PriceCents: price, Quantity: goofishQuantity(data["quantity"]), CategoryID: mtopString(cat["catId"]), CategoryName: mtopString(cat["catName"]),
		Payload: data, RawData: mapFromAny(decoded["data"]),
	}
	if list, _ := data["imageInfoDOList"].([]any); len(list) > 0 {
		result.ImageURL = mtopString(mapFromAny(list[0])["url"])
	}
	return result, nil
}

// GoofishDownshelf 调用 mtop.taobao.idle.item.downshelf 2.0。
func (c *ClientImpl) GoofishDownshelf(ctx context.Context, cookiesStr, itemID string, pace GoofishPaceFunc) error {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return goofishInvalid("downshelf", "item_id 不能为空")
	}
	decoded, _, err := c.goofishCall(ctx, cookiesStr, pace, goofishDownshelfSpec, map[string]any{"itemId": itemID})
	if err != nil {
		return goofishDownshelfRejection(err)
	}
	// data.success=false 且 ret 为 SUCCESS 时，闲鱼业务层明确没有执行下架。
	if data := mapFromAny(decoded["data"]); data != nil {
		if raw, ok := data["success"]; ok && !goofishBool(raw) {
			message := firstNonEmpty(mtopString(data["errorMsg"]), mtopString(data["msg"]), mtopString(data["errorMessage"]), "闲鱼返回 success=false")
			return goofishDownshelfRejection(&GoofishError{Kind: GoofishErrRejected, Stage: "downshelf", Message: "闲鱼拒绝下架：" + sanitizeMTopText(message)})
		}
	}
	return nil
}

// GoofishPolish 擦亮一件商品：先调 mtop.taobao.idle.item.polish 2.0，失败（风控、登录失效、参数错误除外）
// 再调备用 mtop.idle.item.polish 1.0，与上游 PolishItem 相同。返回 alreadyPolished=true 表示闲鱼说今天已经擦亮过，
// 按成功处理。擦亮是幂等的，但结果分类仍与下架一致：主接口结果不明时，备用接口除「不存在」外的失败都算 uncertain。
func (c *ClientImpl) GoofishPolish(ctx context.Context, cookiesStr, itemID string, pace GoofishPaceFunc) (bool, error) {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return false, goofishInvalid("polish", "item_id 不能为空")
	}
	primaryErr := c.goofishPolishOnce(ctx, cookiesStr, pace, goofishPolishSpec, itemID)
	if primaryErr == nil {
		return false, nil
	}
	if duplicatePolishError(primaryErr) {
		return true, nil
	}
	primaryKind := goofishErrKind(primaryErr)
	switch primaryKind {
	case GoofishErrRisk, GoofishErrSessionExpired, GoofishErrInvalid:
		return false, primaryErr
	}
	backupErr := c.goofishPolishOnce(ctx, cookiesStr, pace, goofishPolishBackupSpec, itemID)
	if backupErr == nil {
		return false, nil
	}
	if duplicatePolishError(backupErr) {
		return true, nil
	}
	backupKind := goofishErrKind(backupErr)
	kind := backupKind
	switch {
	case backupKind == GoofishErrNotFound:
	case primaryKind == GoofishErrUncertain:
		// 主接口可能已经擦亮，备用接口的失败不能证明没做成。
		kind = GoofishErrUncertain
	case backupKind == GoofishErrRejected && primaryKind == GoofishErrNotFound:
		kind = GoofishErrNotFound
	}
	combined := &GoofishError{Kind: kind, Stage: "polish", Message: "擦亮主接口失败：" + primaryErr.Error() + "；备用接口失败：" + backupErr.Error(), Err: backupErr}
	var gerr *GoofishError
	if errors.As(backupErr, &gerr) {
		combined.Ret = gerr.Ret
	}
	return false, combined
}

// goofishPolishOnce 调一个擦亮接口；ret SUCCESS 但 data.success=false 算闲鱼拒绝。
func (c *ClientImpl) goofishPolishOnce(ctx context.Context, cookiesStr string, pace GoofishPaceFunc, spec goofishAPI, itemID string) error {
	decoded, _, err := c.goofishCall(ctx, cookiesStr, pace, spec, map[string]any{"itemId": itemID})
	if err != nil {
		return goofishNotFoundFromRejection(err)
	}
	if data := mapFromAny(decoded["data"]); data != nil {
		if raw, ok := data["success"]; ok && !goofishBool(raw) {
			message := firstNonEmpty(mtopString(data["errorMsg"]), mtopString(data["msg"]), mtopString(data["errorMessage"]), mtopString(data["errorCode"]), "闲鱼返回 success=false")
			return goofishNotFoundFromRejection(&GoofishError{Kind: GoofishErrRejected, Stage: "polish", Message: "闲鱼拒绝擦亮：" + sanitizeMTopText(message), Ret: []string{sanitizeMTopText(message)}})
		}
	}
	return nil
}

// goofishErrKind 返回分类错误的 Kind；未分类错误按 rejected。
func goofishErrKind(err error) GoofishErrorKind {
	var gerr *GoofishError
	if errors.As(err, &gerr) {
		return gerr.Kind
	}
	return GoofishErrRejected
}

// GoofishOnSalePage 读取一页在售分组（与上游商品同步使用相同的分组参数），不写本地库。
func (c *ClientImpl) GoofishOnSalePage(ctx context.Context, cookiesStr string, pageNumber int, pace GoofishPaceFunc) (*GoofishOnSalePage, error) {
	if pageNumber < 1 {
		pageNumber = 1
	}
	const pageSize = 20
	signing, _ := mtopRequestCookies(ctx, goofishCurrentCookies(ctx, cookiesStr), "https://www.goofish.com/", goofishH5Base)
	userID := protocol.TransCookies(signing)["unb"]
	if userID == "" {
		return nil, &GoofishError{Kind: GoofishErrSessionExpired, Stage: "item_list", Message: "Cookie 缺少 unb，无法读取在售列表，请重新登录账号"}
	}
	data := map[string]any{
		"needGroupInfo": false, "pageNumber": pageNumber, "pageSize": pageSize,
		"groupName": "在售", "groupId": "58877261", "defaultGroup": true, "userId": userID,
	}
	decoded, _, err := c.goofishCall(ctx, cookiesStr, pace, goofishItemListSpec, data)
	if err != nil {
		return nil, err
	}
	payload := mapFromAny(decoded["data"])
	page := &GoofishOnSalePage{Items: []GoofishOnSaleItem{}}
	for _, item := range parseItemList(payload) {
		page.Items = append(page.Items, GoofishOnSaleItem{ItemID: item.ID, Title: item.Title, Price: item.Price, Status: goofishItemStatus(item.ItemStatus)})
	}
	cards, _ := payload["cardList"].([]any)
	if raw, ok := payload["nextPage"]; ok {
		page.HasMore = goofishBool(raw)
	} else {
		total := itemListReportedTotal(payload)
		page.HasMore = len(cards) >= pageSize && total > pageNumber*pageSize
	}
	return page, nil
}

// goofishItemStatus 把列表卡片 itemStatus 转成文本；0 是在售，其他值原样带出供排查。
func goofishItemStatus(status int) string {
	if status == 0 {
		return "on_sale"
	}
	return "item_status_" + strconv.Itoa(status)
}
