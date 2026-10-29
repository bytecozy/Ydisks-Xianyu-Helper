package mtop

// goofish fork：货源台接口的 publish/edit 载荷构造与文案、字段标准化工具。

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// buildGoofishPublishPayload 按上游已验证的 PC 发布页格式组装 data，类目/属性/发货等取自模板 editDetail。
func buildGoofishPublishPayload(template map[string]any, req GoofishPublishRequest, images []uploadedImage) map[string]any {
	imagePayloads := make([]any, 0, len(images))
	for i, img := range images {
		imagePayloads = append(imagePayloads, publishImagePayload(img, i == 0))
	}
	cat := mapFromAny(template["itemCatDTO"])
	text := goofishCopyMap(mapFromAny(template["itemTextDTO"]))
	separate := goofishBool(text["titleDescSeparate"])
	text["titleDescSeparate"] = separate
	text["title"] = strings.TrimSpace(req.Title)
	text["desc"] = goofishDescFor(separate, req.Title, req.Description)
	labels, _ := template["itemLabelExtList"].([]any)
	if labels == nil {
		labels = []any{}
	}
	protocols := goofishNormalizeProtocols(template["userRightsProtocols"])
	if len(protocols) == 0 {
		protocols = []any{map[string]any{"enable": false, "serviceCode": "SKILL_PLAY_NO_MIND"}}
	}
	data := map[string]any{
		"freebies":            goofishBool(template["freebies"]),
		"itemTypeStr":         firstNonEmpty(mtopString(template["itemTypeStr"]), "b"),
		"quantity":            firstNonEmpty(mtopString(template["quantity"]), "1"),
		"simpleItem":          firstNonEmpty(mtopString(template["simpleItem"]), "true"),
		"imageInfoDOList":     imagePayloads,
		"itemTextDTO":         text,
		"itemLabelExtList":    labels,
		"itemPriceDTO":        map[string]any{"priceInCent": strconv.FormatInt(req.PriceCents, 10)},
		"userRightsProtocols": protocols,
		"itemPostFeeDTO":      goofishNormalizePostFee(template["itemPostFeeDTO"]),
		"defaultPrice":        false,
		"itemCatDTO": map[string]any{
			"catId": mtopString(cat["catId"]), "catName": mtopString(cat["catName"]),
			"channelCatId": mtopString(cat["channelCatId"]), "tbCatId": mtopString(cat["tbCatId"]),
		},
		"uniqueCode":   strconv.FormatInt(time.Now().UnixMicro(), 10),
		"sourceId":     goofishSourceID,
		"bizcode":      goofishSourceID,
		"publishScene": goofishSourceID,
	}
	if addr := mapFromAny(template["itemAddrDTO"]); len(addr) > 0 {
		data["itemAddrDTO"] = goofishCopyMap(addr)
	}
	return data
}

// goofishEditScalarKeys、goofishEditObjectKeys、goofishEditListKeys 是编辑页从 editDetail 带回 edit 的字段；
// 字段表参考 DoLovya/pyxianyu（MIT）对 PC 编辑页 p_publish-index.js 的抓包整理。
var (
	goofishEditScalarKeys = []string{"attribute_biz_line", "bizcode", "bucketId", "canBargain", "defaultPrice", "freebies", "itemTypeStr", "quantity", "scene", "simpleItem", "supportBargainPrice", "topics"}
	goofishEditObjectKeys = []string{"asyncSecurityInfo", "baseParams", "itemAddrDTO", "itemCatDTO", "itemGroupDTO", "itemPostFeeDTO", "itemPriceDTO", "itemTextDTO", "itemTopicParams", "yhbItemInfoDTO"}
	goofishEditListKeys   = []string{"imageInfoDOList", "itemLabelExtList", "itemProperties", "itemSkuList", "userRightsProtocols"}
)

// buildGoofishEditPayload 从 editDetail 构造 edit data，只替换请求给出的字段。
func buildGoofishEditPayload(detail map[string]any, req GoofishEditRequest, images []uploadedImage) (map[string]any, error) {
	data := map[string]any{}
	for _, group := range [][]string{goofishEditScalarKeys, goofishEditObjectKeys, goofishEditListKeys} {
		for _, key := range group {
			if value, ok := detail[key]; ok && value != nil {
				data[key] = goofishDeepCopy(value)
			}
		}
	}
	data["itemId"] = req.ItemID
	data["uniqueCode"] = strconv.FormatInt(time.Now().UnixMicro(), 10)
	data["sourceId"] = firstNonEmpty(mtopString(detail["sourceId"]), goofishSourceID)
	data["bizcode"] = firstNonEmpty(mtopString(detail["bizcode"]), goofishSourceID)
	data["publishScene"] = firstNonEmpty(mtopString(detail["publishScene"]), goofishSourceID)
	if _, ok := data["itemPostFeeDTO"]; ok {
		data["itemPostFeeDTO"] = goofishNormalizePostFee(data["itemPostFeeDTO"])
	}
	if _, ok := data["userRightsProtocols"]; ok {
		data["userRightsProtocols"] = goofishNormalizeProtocols(data["userRightsProtocols"])
	}

	text := goofishCopyMap(mapFromAny(data["itemTextDTO"]))
	separate := goofishBool(text["titleDescSeparate"])
	oldTitle, oldBody := mtopString(text["title"]), mtopString(text["desc"])
	if !separate {
		oldTitle, oldBody = goofishSplitText(oldTitle, oldBody)
	}
	if req.Title != nil || req.Description != nil {
		title, body := oldTitle, oldBody
		if req.Title != nil {
			title = strings.TrimSpace(*req.Title)
		}
		if req.Description != nil {
			body = *req.Description
		}
		if strings.TrimSpace(title) == "" {
			return nil, goofishInvalid("edit", "商品当前没有标题，修改描述时必须同时给 title")
		}
		if err := validateGoofishTitle("edit", title); err != nil {
			return nil, err
		}
		text["title"] = title
		text["desc"] = goofishDescFor(separate, title, body)
		if _, ok := text["titleDescSeparate"]; ok {
			text["titleDescSeparate"] = separate
		}
	}
	if len(text) > 0 {
		data["itemTextDTO"] = text
	}

	if req.PriceCents != nil {
		price := goofishCopyMap(mapFromAny(data["itemPriceDTO"]))
		price["priceInCent"] = strconv.FormatInt(*req.PriceCents, 10)
		// 原价不高于新售价时去掉原价，避免「原价低于售价」被闲鱼拒绝。
		if orig, err := strconv.ParseInt(mtopString(price["origPriceInCent"]), 10, 64); err == nil && orig > 0 && orig <= *req.PriceCents {
			delete(price, "origPriceInCent")
		}
		data["itemPriceDTO"] = price
	}
	if len(images) > 0 {
		list := make([]any, 0, len(images))
		for i, img := range images {
			list = append(list, publishImagePayload(img, i == 0))
		}
		data["imageInfoDOList"] = list
	}
	return data, nil
}

// goofishTemplateUsable 检查模板是否能直接复用。
func goofishTemplateUsable(template map[string]any) error {
	cat := mapFromAny(template["itemCatDTO"])
	if strings.TrimSpace(mtopString(cat["catId"])) == "" || strings.TrimSpace(mtopString(cat["channelCatId"])) == "" {
		return goofishInvalid("publish", "模板商品的 editDetail 缺少类目（itemCatDTO.catId/channelCatId），不能当模板")
	}
	if goofishHasSKUs(template) {
		return goofishInvalid("publish", "模板商品是多规格商品，不能当单价模板")
	}
	return nil
}

// goofishHasSKUs 判断 editDetail 是否带多规格 SKU。
func goofishHasSKUs(detail map[string]any) bool {
	skus, _ := detail["itemSkuList"].([]any)
	return len(skus) > 0
}

// validateGoofishTitle 校验标题：不能为空、不能换行（闲鱼卡片标题取描述第一行）。
func validateGoofishTitle(stage, title string) error {
	if strings.TrimSpace(title) == "" {
		return goofishInvalid(stage, "标题不能为空")
	}
	if strings.ContainsAny(strings.TrimSpace(title), "\r\n") {
		return goofishInvalid(stage, "标题不能换行")
	}
	return nil
}

// goofishComposeText 按网页版约定拼描述：标题在第一行，空一行接正文；正文第一行已是标题时原样使用。
func goofishComposeText(title, body string) string {
	title = strings.TrimSpace(title)
	body = strings.Trim(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	if strings.TrimSpace(body) == "" {
		return title
	}
	firstLine, _, _ := strings.Cut(body, "\n")
	if strings.TrimSpace(firstLine) == title {
		return body
	}
	return title + "\n\n" + body
}

// goofishDescFor 返回 itemTextDTO.desc：标题与描述分开（titleDescSeparate=true）时只放正文，
// 否则按网页版约定把标题放在描述第一行。
func goofishDescFor(separate bool, title, body string) string {
	if separate {
		return strings.Trim(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	}
	return goofishComposeText(title, body)
}

// goofishSplitText 把现有描述拆成标题和正文：第一行等于标题时去掉第一行及其后的空行。
func goofishSplitText(title, desc string) (string, string) {
	desc = strings.ReplaceAll(desc, "\r\n", "\n")
	firstLine, rest, hasRest := strings.Cut(desc, "\n")
	if strings.TrimSpace(title) == "" {
		title = strings.TrimSpace(firstLine)
	}
	if strings.TrimSpace(firstLine) == strings.TrimSpace(title) {
		if !hasRest {
			return title, ""
		}
		return title, strings.TrimLeft(rest, "\n")
	}
	return title, desc
}

// goofishNormalizePostFee 复制发货信息并把布尔字段标准化（editDetail 里常为 "true"/"false" 字符串）。
func goofishNormalizePostFee(value any) map[string]any {
	out := goofishCopyMap(mapFromAny(value))
	for _, key := range []string{"canFreeShipping", "supportFreight", "onlyTakeSelf"} {
		if v, ok := out[key]; ok {
			out[key] = goofishBool(v)
		}
	}
	return out
}

// goofishNormalizeProtocols 复制服务协议列表并标准化 enable。
func goofishNormalizeProtocols(value any) []any {
	list, _ := value.([]any)
	out := make([]any, 0, len(list))
	for _, raw := range list {
		item := mapFromAny(raw)
		if item == nil {
			continue
		}
		copied := goofishCopyMap(item)
		if v, ok := copied["enable"]; ok {
			copied["enable"] = goofishBool(v)
		}
		out = append(out, copied)
	}
	return out
}

// goofishBool 把 bool、"true"/"1" 等转成 bool。
func goofishBool(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		s := strings.TrimSpace(strings.ToLower(v))
		return s == "true" || s == "1"
	case float64:
		return v != 0
	case int:
		return v != 0
	}
	return false
}

// goofishQuantity 解析数量。
func goofishQuantity(value any) int {
	n, err := strconv.Atoi(strings.TrimSpace(mtopString(value)))
	if err != nil || n <= 0 {
		return 1
	}
	return n
}

// goofishCopyMap 浅复制 map；nil 返回空 map。
func goofishCopyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// goofishDeepCopy 通过 JSON 往返深复制 editDetail 字段，避免修改调用方的 map。
func goofishDeepCopy(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var out any
	if json.Unmarshal(raw, &out) != nil {
		return value
	}
	return out
}
