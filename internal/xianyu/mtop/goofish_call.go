package mtop

// goofish fork：货源台接口的 MTOP 调用、Token 重签、风控识别与失败分类。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"time"

	"xianyu-go/internal/xianyu/protocol"
)

// goofishCall 发出一次（含 Token 过期重签）MTOP 调用并把失败分类。
// 只有 ret 为 SUCCESS 时返回 decoded；其余全部返回 *GoofishError。
func (c *ClientImpl) goofishCall(ctx context.Context, cookiesStr string, pace GoofishPaceFunc, spec goofishAPI, data any) (map[string]any, string, error) {
	current := goofishCurrentCookies(ctx, cookiesStr)
	var lastRet []string
	for attempt := 0; attempt < 3; attempt++ {
		if pace != nil {
			if err := pace(ctx); err != nil {
				return nil, current, &GoofishError{Kind: GoofishErrRejected, Stage: spec.stage, Message: spec.api + " 请求未发出：" + err.Error(), Err: err}
			}
		}
		decoded, updated, err := c.goofishCallOnce(ctx, current, spec, data)
		previous := current
		current = goofishPick(current, updated)
		if session := cookieSessionFromContext(ctx); session != nil {
			current, _, _ = session.State()
		}
		if err != nil {
			return nil, current, err
		}
		ret := retFromDecoded(decoded)
		if hasMTopSuccess(ret) {
			return decoded, current, nil
		}
		if !isTokenExpiredRet(ret) {
			return nil, current, classifyGoofishRet(spec, ret)
		}
		// Token 过期：网关在业务之前拒绝，请求没有执行，可以安全重签后重试。
		lastRet = ret
		if attempt == 2 {
			break
		}
		if !mtopTokenCookieChanged(previous, current) {
			refreshed, refreshErr := c.RefreshTokenContext(ctx, current)
			if refreshErr != nil {
				return nil, current, goofishRefreshError(spec, refreshErr)
			}
			current = refreshed.UpdatedCookies
		}
		if err := sleepCtx(ctx, MTopRetryGap); err != nil {
			return nil, current, &GoofishError{Kind: GoofishErrRejected, Stage: spec.stage, Message: spec.api + " 请求未发出：" + err.Error(), Err: err}
		}
	}
	return nil, current, &GoofishError{Kind: GoofishErrRejected, Stage: spec.stage, Message: spec.api + " 登录令牌多次重签仍过期，请求未执行：" + formatMTopRet(lastRet), Ret: sanitizeMTopRet(lastRet)}
}

// goofishCallOnce 发一次请求。返回 decoded（ret 未判定）或已分类的传输/解析错误。
func (c *ClientImpl) goofishCallOnce(ctx context.Context, cookiesStr string, spec goofishAPI, data any) (map[string]any, string, error) {
	rawData, err := json.Marshal(data)
	if err != nil {
		return nil, cookiesStr, goofishInvalid(spec.stage, "请求数据无法序列化："+err.Error())
	}
	endpoint := goofishH5Base + spec.api + "/" + spec.version + "/"
	signingCookies, requestCookies := mtopRequestCookies(ctx, cookiesStr, "https://www.goofish.com/", endpoint)
	t := strconv.FormatInt(time.Now().UnixMilli(), 10)
	sign := protocol.GenerateSign(t, protocol.SignToken(signingCookies), string(rawData))
	query := buildMTopQuery(spec.api, spec.version, t, sign, spec.spmCnt, spec.spmPre, goofishLogID())
	// wrote 记录请求是否已完整写出；没写出的请求肯定没有到达闲鱼。
	wrote := false
	traceCtx := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			wrote = true
		}
	}})
	req, err := http.NewRequestWithContext(traceCtx, http.MethodPost, endpoint+"?"+query, strings.NewReader("data="+url.QueryEscape(string(rawData))))
	if err != nil {
		return nil, cookiesStr, goofishInvalid(spec.stage, "无法构造请求："+err.Error())
	}
	setCommonHeaders(req, requestCookies)
	timeout := 30 * time.Second
	if spec.write {
		timeout = 60 * time.Second
	}
	resp, err := c.httpClientWithTimeout(timeout).Do(req)
	if err != nil {
		if !wrote || !spec.write {
			return nil, cookiesStr, &GoofishError{Kind: GoofishErrRejected, Stage: spec.stage, Message: fmt.Sprintf("%s 请求失败（未送达闲鱼）：%s", spec.api, sanitizeMTopText(err.Error())), Err: err}
		}
		return nil, cookiesStr, &GoofishError{Kind: GoofishErrUncertain, Stage: spec.stage, Message: fmt.Sprintf("%s 请求已发出但没有收到结果：%s", spec.api, sanitizeMTopText(err.Error())), Err: err}
	}
	defer resp.Body.Close()
	updated := absorbMTopResponseCookies(ctx, cookiesStr, resp)
	raw, readErr := readMTopBody(resp)
	if goofishLooksLikeRisk(resp, raw) {
		return nil, updated, &GoofishError{Kind: GoofishErrRisk, Stage: spec.stage, Message: spec.api + " 触发闲鱼风控验证（滑块/punish），未执行，请在闲鱼完成验证后再试", Ret: sanitizeMTopRet(goofishRetFromRaw(raw))}
	}
	if readErr != nil {
		return nil, updated, goofishTransportUncertain(spec, fmt.Sprintf("读取响应失败（HTTP %d）：%s", resp.StatusCode, sanitizeMTopText(readErr.Error())), readErr)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, updated, goofishTransportUncertain(spec, fmt.Sprintf("响应不是 JSON（HTTP %d）：%s", resp.StatusCode, truncate(sanitizeMTopText(string(raw)), 200)), err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		ret := retFromDecoded(decoded)
		if isRiskVerificationRet(ret) || isSessionExpiredRet(ret) || isTokenExpiredRet(ret) {
			return decoded, updated, nil
		}
		return nil, updated, goofishTransportUncertain(spec, fmt.Sprintf("HTTP %d：%s", resp.StatusCode, formatMTopRet(ret)), nil)
	}
	return decoded, updated, nil
}

// goofishTransportUncertain 写请求已送达但结果不明时归为 uncertain；读请求归为失败。
func goofishTransportUncertain(spec goofishAPI, detail string, cause error) error {
	if spec.write {
		return &GoofishError{Kind: GoofishErrUncertain, Stage: spec.stage, Message: spec.api + " 结果不明：" + detail, Err: cause}
	}
	return &GoofishError{Kind: GoofishErrRejected, Stage: spec.stage, Message: spec.api + " 失败：" + detail, Err: cause}
}

// goofishGatewayRejections 是网关在进入业务前就拒绝的 FAIL_SYS 码：请求肯定没有执行。
var goofishGatewayRejections = []string{"FAIL_SYS_API_NOT_FOUNDED", "FAIL_SYS_ILLEGAL_ACCESS", "FAIL_SYS_FLOWLIMIT", "FAIL_SYS_PARAMINVALID", "FAIL_SYS_ILLEGAL_ARGUMENT"}

// classifyGoofishRet 对非 SUCCESS、非 Token 过期的 ret 分类。
func classifyGoofishRet(spec goofishAPI, ret []string) error {
	clean := sanitizeMTopRet(ret)
	text := formatMTopRet(ret)
	switch {
	case isRiskVerificationRet(ret):
		return &GoofishError{Kind: GoofishErrRisk, Stage: spec.stage, Message: spec.api + " 触发闲鱼风控验证，未执行：" + text, Ret: clean}
	case isSessionExpiredRet(ret):
		return &GoofishError{Kind: GoofishErrSessionExpired, Stage: spec.stage, Message: spec.api + " 登录已失效，请重新登录账号：" + text, Ret: clean}
	}
	upper := strings.ToUpper(strings.Join(ret, " "))
	if len(ret) == 0 || strings.Contains(upper, "FAIL_SYS_") {
		gateway := false
		for _, code := range goofishGatewayRejections {
			if strings.Contains(upper, code) {
				gateway = true
				break
			}
		}
		if !gateway {
			if spec.write {
				return &GoofishError{Kind: GoofishErrUncertain, Stage: spec.stage, Message: spec.api + " 闲鱼系统错误，结果不明：" + firstNonEmpty(text, "ret 为空"), Ret: clean}
			}
			return &GoofishError{Kind: GoofishErrRejected, Stage: spec.stage, Message: spec.api + " 闲鱼系统错误：" + firstNonEmpty(text, "ret 为空"), Ret: clean}
		}
	}
	return &GoofishError{Kind: GoofishErrRejected, Stage: spec.stage, Message: "闲鱼拒绝：" + text, Ret: clean}
}

// goofishRefreshError 把 Token 刷新失败映射为风控、登录失效或未执行。
func goofishRefreshError(spec goofishAPI, err error) error {
	switch {
	case IsRiskVerificationErr(err):
		return &GoofishError{Kind: GoofishErrRisk, Stage: spec.stage, Message: spec.api + " 刷新登录令牌时触发风控，未执行：" + sanitizeMTopText(err.Error()), Err: err}
	case IsSessionExpiredErr(err):
		return &GoofishError{Kind: GoofishErrSessionExpired, Stage: spec.stage, Message: spec.api + " 登录已失效，请重新登录账号：" + sanitizeMTopText(err.Error()), Err: err}
	}
	return &GoofishError{Kind: GoofishErrRejected, Stage: spec.stage, Message: spec.api + " 登录令牌过期且刷新失败，未执行：" + sanitizeMTopText(err.Error()), Err: err}
}

// goofishRiskMarkers 是风控拦截页（滑块、punish）在响应体或跳转地址中的特征。
var goofishRiskMarkers = []string{"rgv587", "fail_sys_user_validate", "punish", "captcha", "x5secdata", "x5sec"}

// goofishLooksLikeRisk 判断响应是否为风控拦截页。
func goofishLooksLikeRisk(resp *http.Response, raw []byte) bool {
	if resp != nil && resp.Request != nil && resp.Request.URL != nil && containsAny(strings.ToLower(resp.Request.URL.String()), []string{"punish", "captcha"}) {
		return true
	}
	if len(raw) == 0 {
		return false
	}
	var probe struct {
		Ret []string `json:"ret"`
	}
	if json.Unmarshal(raw, &probe) == nil {
		// 合法 JSON 只看 ret；data 里出现 captcha 等字样（如商品描述）不算风控。
		return isRiskVerificationRet(probe.Ret)
	}
	return containsAny(strings.ToLower(string(raw)), goofishRiskMarkers)
}

// goofishRetFromRaw 尝试从响应体取出 ret。
func goofishRetFromRaw(raw []byte) []string {
	var probe struct {
		Ret []string `json:"ret"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Ret
}

// goofishUploadImages 按顺序上传图片；上传失败时写请求尚未发出，属于肯定没做成。
func (c *ClientImpl) goofishUploadImages(ctx context.Context, cookiesStr string, images []PublishImage, pace GoofishPaceFunc) ([]uploadedImage, string, error) {
	current := cookiesStr
	out := make([]uploadedImage, 0, len(images))
	for i, img := range images {
		if pace != nil {
			if err := pace(ctx); err != nil {
				return nil, current, &GoofishError{Kind: GoofishErrRejected, Stage: "upload", Message: "图片上传未开始：" + err.Error(), Err: err}
			}
		}
		res, updated, err := c.uploadPublishImage(ctx, current, img)
		current = goofishPick(current, updated)
		if err != nil {
			kind := GoofishErrRejected
			message := fmt.Sprintf("第 %d 张图片上传失败，商品没有提交：%s", i+1, sanitizeMTopText(err.Error()))
			if IsRiskVerificationErr(err) {
				kind = GoofishErrRisk
				message = fmt.Sprintf("第 %d 张图片上传触发闲鱼风控，商品没有提交", i+1)
			}
			return nil, current, &GoofishError{Kind: kind, Stage: "upload", Message: message, Err: err}
		}
		out = append(out, res)
	}
	return out, current, nil
}

// goofishNotFoundMarkers 是闲鱼表示商品不存在/已删除/不属于本人的文案或错误码片段。
var goofishNotFoundMarkers = []string{"NOT_EXIST", "NOT_FOUND", "NOTEXIST", "ITEM_DELETED", "不存在", "已删除", "已被删除", "无权", "不是您的"}

// goofishOffShelfMarkers 是下架时商品本来就不在售的文案或错误码片段。
var goofishOffShelfMarkers = []string{"已下架", "已经下架", "下架状态", "非在售", "不在售", "OFF_SHELF", "ALREADY_DOWN", "ITEM_STATUS"}

// goofishNotFoundFromRejection 把业务拒绝中的「商品不存在」改判为 not_found。
func goofishNotFoundFromRejection(err error) error {
	var gerr *GoofishError
	if errors.As(err, &gerr) && gerr.Kind == GoofishErrRejected && len(gerr.Ret) > 0 && containsAny(strings.Join(gerr.Ret, " "), goofishNotFoundMarkers) {
		copied := *gerr
		copied.Kind = GoofishErrNotFound
		copied.Message = "商品不存在或不属于该账号：" + formatMTopRet(gerr.Ret)
		return &copied
	}
	return err
}

// goofishDownshelfRejection 区分下架时的「不存在」「本来就已下架」和其他拒绝。
func goofishDownshelfRejection(err error) error {
	var gerr *GoofishError
	if !errors.As(err, &gerr) || gerr.Kind != GoofishErrRejected {
		return err
	}
	text := strings.Join(append(append([]string{}, gerr.Ret...), gerr.Message), " ")
	switch {
	case containsAny(text, goofishOffShelfMarkers):
		copied := *gerr
		copied.Kind = GoofishErrAlreadyOffShelf
		copied.Message = "商品本来就不在售（已下架或已售出）：" + firstNonEmpty(formatMTopRet(gerr.Ret), gerr.Message)
		return &copied
	case containsAny(text, goofishNotFoundMarkers):
		copied := *gerr
		copied.Kind = GoofishErrNotFound
		copied.Message = "商品不存在或不属于该账号：" + firstNonEmpty(formatMTopRet(gerr.Ret), gerr.Message)
		return &copied
	}
	return err
}

// goofishTemplateError 模板读取失败一律算发布没做成（发布请求还没发）。
func goofishTemplateError(err error) error {
	var gerr *GoofishError
	if !errors.As(err, &gerr) {
		return &GoofishError{Kind: GoofishErrRejected, Stage: "edit_detail", Message: "读取模板商品失败，商品没有发布：" + err.Error(), Err: err}
	}
	copied := *gerr
	switch copied.Kind {
	case GoofishErrNotFound:
		copied.Kind = GoofishErrInvalid
		copied.Message = "模板商品不存在或不属于该账号：" + copied.Message
	case GoofishErrUncertain:
		copied.Kind = GoofishErrRejected
		copied.Message = "读取模板商品失败，商品没有发布：" + copied.Message
	case GoofishErrRejected:
		copied.Message = "读取模板商品失败，商品没有发布：" + copied.Message
	}
	return &copied
}

// goofishStageError 编辑前的 editDetail 失败时编辑请求还没发，不可能是 uncertain。
func goofishStageError(err error, stage string) error {
	var gerr *GoofishError
	if !errors.As(err, &gerr) {
		return &GoofishError{Kind: GoofishErrRejected, Stage: stage, Message: "读取商品详情失败，商品没有修改：" + err.Error(), Err: err}
	}
	copied := *gerr
	if copied.Kind == GoofishErrUncertain {
		copied.Kind = GoofishErrRejected
	}
	if copied.Kind == GoofishErrRejected {
		copied.Message = "读取商品详情失败，商品没有修改：" + copied.Message
	}
	return &copied
}

// goofishCurrentCookies 优先使用上下文会话中的最新 Cookie。
func goofishCurrentCookies(ctx context.Context, fallback string) string {
	if session := cookieSessionFromContext(ctx); session != nil {
		if value, _, _ := session.State(); value != "" {
			return value
		}
	}
	return fallback
}

// goofishPick 返回非空的新 Cookie。
func goofishPick(current, updated string) string {
	if strings.TrimSpace(updated) != "" {
		return updated
	}
	return current
}

// goofishInvalid 构造参数错误。
func goofishInvalid(stage, message string) error {
	return &GoofishError{Kind: GoofishErrInvalid, Stage: stage, Message: message}
}

// goofishItemURL 返回商品网页地址。
func goofishItemURL(itemID string) string {
	return "https://www.goofish.com/item?id=" + itemID
}

// goofishLogID 生成与网页版相同长度的 log_id。
func goofishLogID() string {
	buf := make([]byte, 7)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "goofish0000000"
	}
	return hex.EncodeToString(buf)
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
