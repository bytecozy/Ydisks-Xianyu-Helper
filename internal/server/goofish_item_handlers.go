package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	goofishapp "xianyu-go/internal/application/goofishitems"
	itemapp "xianyu-go/internal/application/items"
	"xianyu-go/internal/auth"
)

// GoofishItemsPort 是货源台专用商品接口的应用端口（goofish fork）。
type GoofishItemsPort = goofishapp.Port

// goofishItemsApplication 返回货源台商品端口；未装配时为 nil。
func (s *Server) goofishItemsApplication() GoofishItemsPort {
	return s.applicationServiceSet().goofishItems
}

// mountVersionedGoofishItems 挂载货源台专用商品接口，鉴权与其他 /api/v1 接口相同。
func (s *Server) mountVersionedGoofishItems(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(s.Auth.Middleware)
		r.Use(auth.RequireAuth)
		r.Get("/api/v1/goofish/items/{cookie_id}/edit-detail", s.goofishEditDetail)
		r.Get("/api/v1/goofish/items/{cookie_id}/on-sale", s.goofishOnSale)
		r.Post("/api/v1/goofish/items/publish", s.goofishPublish)
		r.Post("/api/v1/goofish/items/{cookie_id}/{item_id}/edit", s.goofishEdit)
		r.Post("/api/v1/goofish/items/{cookie_id}/{item_id}/downshelf", s.goofishDownshelf)
		r.Post("/api/v1/goofish/items/{cookie_id}/{item_id}/polish", s.goofishPolish)
	})
}

// goofishPort 返回端口；未装配时写 503。
func (s *Server) goofishPort(w http.ResponseWriter) (GoofishItemsPort, bool) {
	port := s.goofishItemsApplication()
	if port == nil {
		writeErrCode(w, http.StatusServiceUnavailable, "goofish_items_unavailable", "货源台商品接口未启用", "")
		return nil, false
	}
	return port, true
}

// goofishOwnedAccount 校验账号归属；失败时已写响应。
func (s *Server) goofishOwnedAccount(w http.ResponseWriter, r *http.Request, cookieID string) (int64, bool) {
	if strings.TrimSpace(cookieID) == "" {
		writeErrCode(w, http.StatusBadRequest, "invalid_request", "缺少 cookie_id", "")
		return 0, false
	}
	_, userID, ok := s.cookieForCurrentUser(w, r, cookieID)
	return userID, ok
}

// goofishEditDetail 原样返回 editDetail 的 data（只读）。
func (s *Server) goofishEditDetail(w http.ResponseWriter, r *http.Request) {
	port, ok := s.goofishPort(w)
	if !ok {
		return
	}
	cookieID := chi.URLParam(r, "cookie_id")
	itemID := strings.TrimSpace(r.URL.Query().Get("item_id"))
	if itemID == "" {
		writeErrCode(w, http.StatusBadRequest, "invalid_request", "缺少 item_id", "")
		return
	}
	userID, ok := s.goofishOwnedAccount(w, r, cookieID)
	if !ok {
		return
	}
	data, err := port.EditDetail(r.Context(), userID, cookieID, itemID)
	if err != nil {
		s.writeGoofishError(w, "edit_detail", err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

// goofishOnSale 返回一页在售商品。
func (s *Server) goofishOnSale(w http.ResponseWriter, r *http.Request) {
	port, ok := s.goofishPort(w)
	if !ok {
		return
	}
	cookieID := chi.URLParam(r, "cookie_id")
	page := 1
	if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			writeErrCode(w, http.StatusBadRequest, "invalid_request", "page 必须是 1–500 的整数", "")
			return
		}
		page = parsed
	}
	userID, ok := s.goofishOwnedAccount(w, r, cookieID)
	if !ok {
		return
	}
	result, err := port.OnSale(r.Context(), userID, cookieID, page)
	if err != nil {
		s.writeGoofishError(w, "on_sale", err)
		return
	}
	items := result.Items
	if items == nil {
		items = []goofishapp.OnSaleItem{}
	}
	writeJSON(w, http.StatusOK, goofishOnSaleResponse{Success: true, Items: items, HasMore: result.HasMore, Page: page})
}

// goofishPublish 按模板商品发布新商品。
func (s *Server) goofishPublish(w http.ResponseWriter, r *http.Request) {
	port, ok := s.goofishPort(w)
	if !ok {
		return
	}
	if !parseMultipartRequest(w, r, maxItemPublishBytes, maxOrderImportBytes, "商品发布图片总大小不能超过 96 MiB") {
		return
	}
	cookieID := strings.TrimSpace(r.FormValue("cookie_id"))
	templateItemID := strings.TrimSpace(r.FormValue("template_item_id"))
	title := strings.TrimSpace(r.FormValue("title"))
	description := r.FormValue("description")
	if templateItemID == "" || title == "" {
		writeErrCode(w, http.StatusBadRequest, "invalid_request", "template_item_id 和 title 必填", "")
		return
	}
	priceCents, err := parseMoneyCents(r.FormValue("price"))
	if err != nil || priceCents <= 0 {
		writeErrCode(w, http.StatusBadRequest, "invalid_request", "price 必须是大于 0 的金额（元，最多两位小数）", "")
		return
	}
	images, err := readPublishImageField(r, "images", goofishMaxImages, true)
	if err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid_request", err.Error(), "")
		return
	}
	userID, ok := s.goofishOwnedAccount(w, r, cookieID)
	if !ok {
		return
	}
	result, err := port.Publish(r.Context(), goofishapp.PublishInput{
		UserID: userID, CookieID: cookieID, TemplateItemID: templateItemID, Title: title,
		Description: description, PriceCents: priceCents, Images: goofishAppImages(images),
	})
	if err != nil {
		s.writeGoofishError(w, "publish", err)
		return
	}
	writeJSON(w, http.StatusOK, goofishWriteResponse{Success: true, ItemID: result.ItemID, ItemURL: result.ItemURL, Warnings: result.Warnings})
}

// goofishEdit 修改已有商品的标题、描述、价格或图片。
func (s *Server) goofishEdit(w http.ResponseWriter, r *http.Request) {
	port, ok := s.goofishPort(w)
	if !ok {
		return
	}
	if !parseMultipartRequest(w, r, maxItemPublishBytes, maxOrderImportBytes, "商品图片总大小不能超过 96 MiB") {
		return
	}
	cookieID, itemID := chi.URLParam(r, "cookie_id"), strings.TrimSpace(chi.URLParam(r, "item_id"))
	input := goofishapp.EditInput{CookieID: cookieID, ItemID: itemID}
	if values, present := r.MultipartForm.Value["title"]; present && len(values) > 0 {
		title := strings.TrimSpace(values[0])
		if title == "" {
			writeErrCode(w, http.StatusBadRequest, "invalid_request", "title 不能为空", "")
			return
		}
		input.Title = &title
	}
	if values, present := r.MultipartForm.Value["description"]; present && len(values) > 0 {
		description := values[0]
		input.Description = &description
	}
	if values, present := r.MultipartForm.Value["price"]; present && len(values) > 0 {
		priceCents, err := parseMoneyCents(values[0])
		if err != nil || priceCents <= 0 {
			writeErrCode(w, http.StatusBadRequest, "invalid_request", "price 必须是大于 0 的金额（元，最多两位小数）", "")
			return
		}
		input.PriceCents = &priceCents
	}
	images, err := readPublishImageField(r, "images", goofishMaxImages, false)
	if err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid_request", err.Error(), "")
		return
	}
	input.Images = goofishAppImages(images)
	if input.Title == nil && input.Description == nil && input.PriceCents == nil && len(input.Images) == 0 {
		writeErrCode(w, http.StatusBadRequest, "invalid_request", "至少给出 title、description、price、images 之一", "")
		return
	}
	userID, ok := s.goofishOwnedAccount(w, r, cookieID)
	if !ok {
		return
	}
	input.UserID = userID
	result, err := port.Edit(r.Context(), input)
	if err != nil {
		s.writeGoofishError(w, "edit", err)
		return
	}
	writeJSON(w, http.StatusOK, goofishWriteResponse{Success: true, ItemID: result.ItemID, Warnings: result.Warnings})
}

// goofishDownshelf 下架商品。
func (s *Server) goofishDownshelf(w http.ResponseWriter, r *http.Request) {
	port, ok := s.goofishPort(w)
	if !ok {
		return
	}
	cookieID, itemID := chi.URLParam(r, "cookie_id"), strings.TrimSpace(chi.URLParam(r, "item_id"))
	userID, ok := s.goofishOwnedAccount(w, r, cookieID)
	if !ok {
		return
	}
	result, err := port.Downshelf(r.Context(), userID, cookieID, itemID)
	if err != nil {
		s.writeGoofishError(w, "downshelf", err)
		return
	}
	writeJSON(w, http.StatusOK, goofishWriteResponse{Success: true, ItemID: result.ItemID, Status: "off_shelf", Warnings: result.Warnings})
}

// goofishPolish 擦亮商品；今天已经擦亮过也算成功（status=already_polished）。
func (s *Server) goofishPolish(w http.ResponseWriter, r *http.Request) {
	port, ok := s.goofishPort(w)
	if !ok {
		return
	}
	cookieID, itemID := chi.URLParam(r, "cookie_id"), strings.TrimSpace(chi.URLParam(r, "item_id"))
	userID, ok := s.goofishOwnedAccount(w, r, cookieID)
	if !ok {
		return
	}
	result, err := port.Polish(r.Context(), userID, cookieID, itemID)
	if err != nil {
		s.writeGoofishError(w, "polish", err)
		return
	}
	status := "polished"
	if result.AlreadyPolished {
		status = "already_polished"
	}
	writeJSON(w, http.StatusOK, goofishWriteResponse{Success: true, ItemID: result.ItemID, Status: status, Warnings: result.Warnings})
}

// goofishWriteResponse 是发布/编辑/下架/擦亮的成功响应；本地同步问题放在 warnings，不影响成功语义。
type goofishWriteResponse struct {
	// Success 恒为 true。
	Success bool `json:"success"`
	// ItemID 是闲鱼商品 ID。
	ItemID string `json:"item_id"`
	// ItemURL 是商品网页地址（发布时返回）。
	ItemURL string `json:"item_url,omitempty"`
	// Status 是下架后的 off_shelf，或擦亮后的 polished / already_polished。
	Status string `json:"status,omitempty"`
	// Warnings 是闲鱼已成功但本地同步失败的说明。
	Warnings []string `json:"warnings,omitempty"`
}

// goofishOnSaleResponse 是在售列表一页的响应。
type goofishOnSaleResponse struct {
	// Success 恒为 true。
	Success bool `json:"success"`
	// Items 是本页商品。
	Items []goofishapp.OnSaleItem `json:"items"`
	// HasMore 表示还有下一页。
	HasMore bool `json:"has_more"`
	// Page 是本页页码。
	Page int `json:"page"`
}

// writeGoofishError 按货源台错误约定写响应：
// 4xx 肯定没改到闲鱼；409 risk_control 风控；502 *_failed 明确拒绝；502 *_uncertain 结果不明。
func (s *Server) writeGoofishError(w http.ResponseWriter, op string, err error) {
	var appErr *goofishapp.Error
	if !errors.As(err, &appErr) {
		appErr = &goofishapp.Error{Op: op, Kind: goofishapp.KindRejected, Message: err.Error()}
	}
	status, code := goofishStatus(op, appErr.Kind)
	message := strings.TrimSpace(appErr.Message)
	if message == "" {
		message = code
	}
	if s.Logger != nil && status >= http.StatusInternalServerError {
		s.Logger.Error("货源台商品接口失败", "op", op, "code", code, "err", message)
	}
	writeErrCode(w, status, code, message, "")
}

// goofishStatus 把失败分类映射为 HTTP 状态和 code。
func goofishStatus(op string, kind goofishapp.ErrorKind) (int, string) {
	switch kind {
	case goofishapp.KindInvalid:
		return http.StatusBadRequest, "invalid_request"
	case goofishapp.KindNotFound:
		return http.StatusNotFound, "item_not_found"
	case goofishapp.KindAlreadyOffShelf:
		return http.StatusConflict, "already_off_shelf"
	case goofishapp.KindAccountOffline:
		return http.StatusConflict, "account_offline"
	case goofishapp.KindRisk:
		return http.StatusConflict, "risk_control"
	case goofishapp.KindUncertain:
		return http.StatusBadGateway, op + "_uncertain"
	default:
		return http.StatusBadGateway, op + "_failed"
	}
}

// goofishAppImages 转换上传图片。
func goofishAppImages(images []itemapp.Image) []goofishapp.Image {
	out := make([]goofishapp.Image, 0, len(images))
	for _, img := range images {
		out = append(out, goofishapp.Image{Filename: img.Filename, ContentType: img.ContentType, Data: img.Data})
	}
	return out
}

// goofishMaxImages 是单个商品最多的图片数。
const goofishMaxImages = 9
