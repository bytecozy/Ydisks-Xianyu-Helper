// Package goofishitems 定义货源台（goofish fork）专用闲鱼商品接口的应用层模型与端口。
//
// 只在 bytecozy/Ydisks-Xianyu-Helper 的 goofish 分支存在；HTTP 层和适配器通过本包交换数据，
// 互不依赖对方的具体类型。
package goofishitems

import "context"

// ErrorKind 是失败分类；HTTP 层据此映射状态码和 code 后缀。
type ErrorKind string

const (
	// KindInvalid：参数或模板不可用（400），肯定没有改到闲鱼。
	KindInvalid ErrorKind = "invalid"
	// KindNotFound：商品不存在或不属于该账号（404）。
	KindNotFound ErrorKind = "not_found"
	// KindAlreadyOffShelf：下架时商品本来就不在售（409）。
	KindAlreadyOffShelf ErrorKind = "already_off_shelf"
	// KindAccountOffline：账号凭证不可用或登录失效（409）。
	KindAccountOffline ErrorKind = "account_offline"
	// KindRisk：触发风控（409 risk_control），没做成。
	KindRisk ErrorKind = "risk_control"
	// KindRejected：闲鱼明确拒绝或请求没发出（502 *_failed），没做成。
	KindRejected ErrorKind = "rejected"
	// KindUncertain：请求已发出但结果不明（502 *_uncertain）。
	KindUncertain ErrorKind = "uncertain"
)

// Error 是带操作名的分类错误。
type Error struct {
	// Op 是操作：publish、edit、downshelf、on_sale、edit_detail。
	Op string
	// Kind 是失败分类。
	Kind ErrorKind
	// Message 是带闲鱼原文（已脱敏）的说明。
	Message string
	// Err 保留底层错误链。
	Err error
}

// Error 返回说明文本。
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Unwrap 返回底层错误。
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Image 是上传的一张图片。
type Image struct {
	Filename    string
	ContentType string
	Data        []byte
}

// PublishInput 是按模板发布的输入。
type PublishInput struct {
	UserID         int64
	CookieID       string
	TemplateItemID string
	Title          string
	Description    string
	PriceCents     int64
	Images         []Image
}

// EditInput 是编辑已有商品的输入；nil/空表示不改。
type EditInput struct {
	UserID      int64
	CookieID    string
	ItemID      string
	Title       *string
	Description *string
	PriceCents  *int64
	Images      []Image
}

// WriteResult 是发布/编辑/下架成功后的结果。Warnings 记录闲鱼已成功但本地同步失败的情况。
type WriteResult struct {
	ItemID   string
	ItemURL  string
	Warnings []string
}

// OnSaleItem 是在售列表的一行。
type OnSaleItem struct {
	ItemID string `json:"item_id"`
	Title  string `json:"title"`
	Price  string `json:"price"`
	Status string `json:"status"`
}

// OnSalePage 是在售列表的一页。
type OnSalePage struct {
	Items   []OnSaleItem
	HasMore bool
}

// Port 是货源台商品接口的基础设施端口。
type Port interface {
	EditDetail(ctx context.Context, userID int64, cookieID, itemID string) (map[string]any, error)
	Publish(ctx context.Context, input PublishInput) (WriteResult, error)
	Edit(ctx context.Context, input EditInput) (WriteResult, error)
	Downshelf(ctx context.Context, userID int64, cookieID, itemID string) (WriteResult, error)
	OnSale(ctx context.Context, userID int64, cookieID string, page int) (OnSalePage, error)
}
