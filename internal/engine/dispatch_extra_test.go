package engine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"xianyu-go/internal/automation"
)

// TestHandleMessage_SystemEventRoutesToAutomation 付款待发货系统卡片进入自动化中心，不进入回复链。
func TestHandleMessage_SystemEventRoutesToAutomation(t *testing.T) {
	// acc、h、cleanup 用于本次流程后续判断的acc、h、cleanup
	acc, h, _, cleanup := newAccountForTest(t)
	defer cleanup()
	defer acc.Stop()

	// decrypted 用于本次流程后续判断的decrypted
	decrypted := mustPaidDeliveryCard(t)
	acc.handleMessage(decrypted)

	// 系统事件应交给 handler.HandleSystemEvent（recordingHandler 目前不记录系统事件，
	// 但自动化 ExtractTaskFromWS 应识别为 TriggerOrderPaid——这里通过 handler 间接断言）。
	// 直接验证：handleMessage 不应触发任何防抖回复投递。
	time.Sleep(MessageDebounceDelay + 200*time.Millisecond)
	h.mu.Lock()
	// chats 用于本次流程后续判断的chats
	chats := len(h.chats)
	h.mu.Unlock()
	if chats != 0 {
		t.Errorf("系统卡片不应进入回复链，got %d 条聊天投递", chats)
	}
}

// systemCapturingHandler 捕获系统事件，用于断言 handleMessage 把系统卡片交给自动化。
type systemCapturingHandler struct {
	recordingHandler
	mu    sync.Mutex
	tasks []automation.Task
}

// HandleSystemEvent 处理系统Event。
func (s *systemCapturingHandler) HandleSystemEvent(_ context.Context, task automation.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks = append(s.tasks, task)
	return nil
}

// TestHandleMessage_SystemEventDispatchedToHandler 系统卡片经 handleMessage 进入 handler.HandleSystemEvent。
func TestHandleMessage_SystemEventDispatchedToHandler(t *testing.T) {
	// acc、cleanup 用于本次流程后续判断的acc、cleanup
	acc, _, _, cleanup := newAccountForTest(t)
	defer cleanup()
	defer acc.Stop()

	// h 用于本次流程后续判断的h
	h := &systemCapturingHandler{}
	acc.handler = h
	acc.handleMessage(mustPaidDeliveryCard(t))

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.tasks) != 1 {
		t.Fatalf("应投递 1 个系统事件，got %d", len(h.tasks))
	}
	if h.tasks[0].TriggerType != automation.TriggerOrderPaid {
		t.Errorf("TriggerType=%q want %q", h.tasks[0].TriggerType, automation.TriggerOrderPaid)
	}
}

// TestHandleMessage_PlainChatRoutesToDebounce 普通聊天消息进入防抖回复链。
func TestHandleMessage_PlainChatRoutesToDebounce(t *testing.T) {
	// acc、h、cleanup 用于本次流程后续判断的acc、h、cleanup
	acc, h, _, cleanup := newAccountForTest(t)
	defer cleanup()
	defer acc.Stop()

	// 构造一条普通聊天消息（非系统卡片、非 contentType 14/26）。
	decrypted := plainChatMessage(t, "你好老板", "buyer1", "chat-plain")
	acc.handleMessage(decrypted)

	time.Sleep(MessageDebounceDelay + 200*time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.chats) != 1 {
		t.Fatalf("应投递 1 条聊天消息，got %d", len(h.chats))
	}
	if h.chats[0].Text != "你好老板" {
		t.Errorf("Text=%q want 你好老板", h.chats[0].Text)
	}
}

// TestHandleMessage_ContentType14Filtered contentType=14 系统提示不进入回复链。
func TestHandleMessage_ContentType14Filtered(t *testing.T) {
	// acc、h、cleanup 用于本次流程后续判断的acc、h、cleanup
	acc, h, _, cleanup := newAccountForTest(t)
	defer cleanup()
	defer acc.Stop()

	acc.handleMessage(mustContentType14Notice(t))
	time.Sleep(MessageDebounceDelay + 200*time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.chats) != 0 {
		t.Errorf("contentType=14 不应进入回复链，got %d", len(h.chats))
	}
}

// TestHandleMessage_RefundCardFiltered contentType=26 退款卡片不进入回复链。
func TestHandleMessage_RefundCardFiltered(t *testing.T) {
	// acc、h、cleanup 用于本次流程后续判断的acc、h、cleanup
	acc, h, _, cleanup := newAccountForTest(t)
	defer cleanup()
	defer acc.Stop()

	acc.handleMessage(mustRefundTradeCard(t))
	time.Sleep(MessageDebounceDelay + 200*time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.chats) != 0 {
		t.Errorf("退款卡片不应进入回复链，got %d", len(h.chats))
	}
}

// TestHandleMessage_DedupSkipsRepeat 同一 msgID 的重复消息只处理一次。
func TestHandleMessage_DedupSkipsRepeat(t *testing.T) {
	// acc、h、cleanup 用于本次流程后续判断的acc、h、cleanup
	acc, h, _, cleanup := newAccountForTest(t)
	defer cleanup()
	defer acc.Stop()

	// decrypted 用于本次流程后续判断的decrypted
	decrypted := plainChatMessage(t, "重复消息", "buyer1", "chat-dup")
	acc.handleMessage(decrypted)
	acc.handleMessage(decrypted) // 重复
	acc.handleMessage(decrypted) // 再次重复

	time.Sleep(MessageDebounceDelay + 200*time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.chats) != 1 {
		t.Errorf("去重后应只投递 1 条，got %d", len(h.chats))
	}
}

// TestHandleMessage_RecordsOwnWebSocketEchoWithoutAutoReply 验证官方客户端回显会走出站观察持久化，但绝不触发自动回复。
func TestHandleMessage_RecordsOwnWebSocketEchoWithoutAutoReply(t *testing.T) {
	// acc 是待测账号；h 记录业务处理调用；cleanup 释放测试数据库和运行时资源。
	acc, h, _, cleanup := newAccountForTest(t)
	defer cleanup()
	defer acc.Stop()

	// decrypted 模拟官方客户端发送的文本回显，深链提供真实买家身份，PNM 键用于后续持久化幂等。
	decrypted := plainChatMessage(t, "官方客户端发送", "123", "chat-own")
	// envelope 保存可变测试消息信封，用于补齐官方回显的 PNM 键和对端用户标识。
	envelope := decrypted["1"].(map[string]any)
	envelope["3"] = "own-message.PNM"
	// extension 保存消息展示扩展；深链中的 peerUserId 必须指向买家而不是当前账号。
	extension := envelope["10"].(map[string]any)
	extension["reminderUrl"] = "fleamarket://message_chat?itemId=item-chat-own&peerUserId=buyer-own"
	acc.handleMessage(decrypted)
	time.Sleep(MessageDebounceDelay + 200*time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.chats) != 0 {
		t.Fatalf("账号自身消息回显不应触发自动回复，got %d", len(h.chats))
	}
	if len(h.outgoing) != 1 {
		t.Fatalf("账号自身消息回显应交给出站观察器，got %d", len(h.outgoing))
	}
	// echo 保存观察器收到的出站回显，用于断言会话、买家和平台幂等键未丢失。
	echo := h.outgoing[0]
	if echo.AccountID != "cid" || echo.ChatID != "chat-own" || echo.BuyerID != "buyer-own" || echo.MessageKey != "own-message.PNM" || echo.Text != "官方客户端发送" {
		t.Fatalf("官方客户端出站回显字段错误: %+v", echo)
	}
}

// TestHandleMessagePersistsEachIncomingFrame 验证同一聊天的询价和紧随其后的平台提示都交给持久化端口。
func TestHandleMessagePersistsEachIncomingFrame(t *testing.T) {
	// acc 使用真实消息分发；handler 记录交给持久化端口的消息；cleanup 关闭测试数据库。
	acc, handler, _, cleanup := newAccountForTest(t)
	defer cleanup()
	defer acc.Stop()

	// inquiry 是首次实时到达的询价正文。
	inquiry := plainChatMessage(t, "hi 請問這個號最便宜是多少", "buyer-1", "chat-inquiry")
	// notice 是紧接正文到达、具有不同平台消息键的安全提示。
	notice := plainChatMessage(t, "[注意！小心假客服骗钱！]", "buyer-1", "chat-inquiry")
	inquiry["1"].(map[string]any)["3"] = "inquiry.PNM"
	notice["1"].(map[string]any)["3"] = "notice.PNM"

	acc.handleMessage(inquiry)
	acc.handleMessage(notice)
	acc.handleMessage(inquiry)

	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.chats) != 2 {
		t.Fatalf("两条不同消息都应保存，重复帧应去重：实际保存 %d 条", len(handler.chats))
	}
	if handler.chats[0].Text != "hi 請問這個號最便宜是多少" || handler.chats[1].Text != "[注意！小心假客服骗钱！]" {
		t.Fatal("询价正文与后续提示的持久化顺序错误")
	}
}

// TestHandleMessage_NoReminderNoOp 无 reminderContent 的消息不进入回复链也不报错。
func TestHandleMessage_NoReminderNoOp(t *testing.T) {
	// acc、h、cleanup 用于本次流程后续判断的acc、h、cleanup
	acc, h, _, cleanup := newAccountForTest(t)
	defer cleanup()
	defer acc.Stop()

	// 缺少 reminderContent 的消息。
	decrypted := map[string]any{
		"1": map[string]any{
			"2": "123@goofish",
			"10": map[string]any{
				"senderUserId": "b1",
			},
		},
	}
	acc.handleMessage(decrypted)
	time.Sleep(MessageDebounceDelay + 200*time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.chats) != 0 {
		t.Errorf("无 reminder 不应投递，got %d", len(h.chats))
	}
}

// TestDispatch_UpdatesLastMsgReceived dispatch 记录消息接收时间。
func TestDispatch_UpdatesLastMsgReceived(t *testing.T) {
	// acc、cleanup 用于本次流程后续判断的acc、cleanup
	acc, _, _, cleanup := newAccountForTest(t)
	defer cleanup()
	defer acc.Stop()

	acc.mu.Lock()
	acc.lastMsgReceived = time.Time{}
	acc.mu.Unlock()

	// before 用于本次流程后续判断的before
	before := time.Now()
	acc.dispatch(plainChatMessage(t, "hi", "b1", "c1"))
	// dispatch 内部起 goroutine，等其更新 lastMsgReceived。
	time.Sleep(50 * time.Millisecond)

	acc.mu.Lock()
	// last 用于本次流程后续判断的last
	last := acc.lastMsgReceived
	acc.mu.Unlock()
	if last.IsZero() || last.Before(before) {
		t.Errorf("dispatch 应更新 lastMsgReceived，got %v", last)
	}
}

// TestDispatch_SemaphoreOverflowDrops 消息并发达上限时丢弃多余消息（不阻塞）。
func TestDispatch_SemaphoreOverflowDrops(t *testing.T) {
	// acc、cleanup 用于本次流程后续判断的acc、cleanup
	acc, _, _, cleanup := newAccountForTest(t)
	defer cleanup()
	defer acc.Stop()

	// 把信号量填满，再投递一条：应走 default 分支丢弃，不阻塞。
	for i := 0; i < MessageSemaphoreSize; i++ {
		acc.sem <- struct{}{}
	}
	// 这条应被丢弃（dispatch 立即返回，不进入 handleMessage）。
	done := make(chan struct{})
	go func() {
		acc.dispatch(plainChatMessage(t, "overflow", "b1", "c1"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatch 在信号量满时应立即返回，不应阻塞")
	}

	// 释放信号量，避免 Stop 时泄漏 goroutine。
	for i := 0; i < MessageSemaphoreSize; i++ {
		<-acc.sem
	}
}

// TestDispatch_SystemEventWaitsForCapacityInsteadOfDropping 封装TestDispatch系统EventWaitsForCapacityInsteadOfDropping业务协调。
func TestDispatch_SystemEventWaitsForCapacityInsteadOfDropping(t *testing.T) {
	// acc、cleanup 用于本次流程后续判断的acc、cleanup
	acc, _, _, cleanup := newAccountForTest(t)
	defer cleanup()
	defer acc.Stop()
	// h 用于本次流程后续判断的h
	h := &systemCapturingHandler{}
	acc.handler = h
	for // i 用于本次流程后续判断的i
	i := 0; i < MessageSemaphoreSize; i++ {
		acc.sem <- struct{}{}
	}
	// event 用于本次流程后续判断的event
	event := mustPaidDeliveryCard(t)
	// done 用于本次流程后续判断的done
	done := make(chan struct{})
	go func() {
		acc.dispatch(event)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("system event was dropped while semaphore was full")
	case <-time.After(100 * time.Millisecond):
	}
	<-acc.sem
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("system event did not continue after capacity became available")
	}
	// deadline 用于本次流程后续判断的deadline
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		// count 用于本次流程后续判断的数量
		count := len(h.tasks)
		h.mu.Unlock()
		if count == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.mu.Lock()
	// count 用于本次流程后续判断的数量
	count := len(h.tasks)
	h.mu.Unlock()
	if count != 1 {
		t.Fatalf("system event count=%d want 1", count)
	}
	for len(acc.sem) > 0 {
		<-acc.sem
	}
}

// TestScheduleDebouncedReply_DifferentChatIDsNotCoalesced 验证两个会话拥有各自独立的回复计时。
func TestScheduleDebouncedReply_DifferentChatIDsNotCoalesced(t *testing.T) {
	// dispatcher 保留每个会话的自动回复定时器。
	dispatcher := newMessageDispatcher(messageDispatcherConfig{CookieID: "cid"})
	defer dispatcher.stop()
	dispatcher.scheduleDebouncedReply(ChatMessage{ChatID: "chat-a", Text: "A1"})
	dispatcher.scheduleDebouncedReply(ChatMessage{ChatID: "chat-b", Text: "B1"})

	dispatcher.debounceMu.Lock()
	defer dispatcher.debounceMu.Unlock()
	if len(dispatcher.debounceTimers) != 2 || dispatcher.debounceTimers["chat-a"].lastMsg.Text != "A1" || dispatcher.debounceTimers["chat-b"].lastMsg.Text != "B1" {
		t.Fatal("两个会话应各自保留待回复消息")
	}
}

// TestScheduleDebouncedReply_TimerReset 验证连续买家消息重置回复计时并替换待回复内容。
func TestScheduleDebouncedReply_TimerReset(t *testing.T) {
	// dispatcher 保存同一会话的回复计时，为测试隔离账号外部依赖。
	dispatcher := newMessageDispatcher(messageDispatcherConfig{CookieID: "cid"})
	defer dispatcher.stop()
	dispatcher.scheduleDebouncedReply(ChatMessage{ChatID: "chat-reset", Text: "first"})
	dispatcher.debounceMu.Lock()
	// initial 是第一条消息的定时器，第二条到达后不再使用。
	initial := dispatcher.debounceTimers["chat-reset"]
	dispatcher.debounceMu.Unlock()
	dispatcher.scheduleDebouncedReply(ChatMessage{ChatID: "chat-reset", Text: "second"})

	dispatcher.debounceMu.Lock()
	defer dispatcher.debounceMu.Unlock()
	// latest 是第二条消息替换后的待回复值及定时器。
	latest := dispatcher.debounceTimers["chat-reset"]
	if latest == initial || latest.lastMsg.Text != "second" || !latest.deadline.After(initial.deadline) {
		t.Fatal("后续消息应替换内容并延后回复")
	}
}

// TestScheduleDebouncedReply_StopClearsTimers 验证停机取消未开始的自动回复，并清空待回复表。
func TestScheduleDebouncedReply_StopClearsTimers(t *testing.T) {
	// started 记录定时器是否在停机后仍尝试开始自动回复。
	started := make(chan struct{}, 1)
	// dispatcher 用生命周期回调观测取消，不启动真实账号。
	dispatcher := newMessageDispatcher(messageDispatcherConfig{BeginTask: func() (context.Context, func(), bool) {
		started <- struct{}{}
		return nil, nil, false
	}})
	dispatcher.scheduleDebouncedReply(ChatMessage{ChatID: "chat-stop", Text: "hi"})
	dispatcher.stop()
	select {
	case <-started:
		t.Fatal("停机后的定时器仍尝试自动回复")
	case <-time.After(MessageDebounceDelay + 100*time.Millisecond):
	}
}

// cancelAwareHandler 用于本次流程后续判断的取消AwareHandler
type cancelAwareHandler struct {
	started  chan struct{}
	canceled chan struct{}
}

// HandleChatMessage 处理聊天消息。
func (h *cancelAwareHandler) HandleChatMessage(ctx context.Context, _ ChatMessage) error {
	close(h.started)
	<-ctx.Done()
	close(h.canceled)
	return ctx.Err()
}

// HandleSystemEvent 处理系统Event。
func (h *cancelAwareHandler) HandleSystemEvent(context.Context, automation.Task) error { return nil }

// OnPasswordLoginRefresh 封装On密码登录Refresh业务协调。
func (h *cancelAwareHandler) OnPasswordLoginRefresh(context.Context, string) bool { return false }

// OnAccountAlert 封装On账号Alert业务协调。
func (h *cancelAwareHandler) OnAccountAlert(context.Context, string, string, string, string) {}

// TestStopCancelsInFlightChatHandler 验证停机取消正在处理的入站聊天。
func TestStopCancelsInFlightChatHandler(t *testing.T) {
	// handler 用于本次流程后续判断的handler
	handler := &cancelAwareHandler{started: make(chan struct{}), canceled: make(chan struct{})}
	// acc 用于本次流程后续判断的acc
	acc := New(Config{CookieID: "cid", CookieStr: "unb=1", Handler: handler})
	acc.reply = nil
	// ctx、cancel 用于本次流程后续判断的ctx、cancel
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	acc.lifecycle.start(ctx, cancel)
	// frame 模拟已接纳且持久化尚未完成的买家聊天。
	frame := plainChatMessage(t, "hi", "buyer", "cancel-chat")
	go acc.handleMessage(frame)
	select {
	case <-handler.started:
	case <-time.After(MessageDebounceDelay + time.Second):
		t.Fatal("handler did not start")
	}
	// done 用于本次流程后续判断的done
	done := make(chan struct{})
	go func() {
		acc.Stop()
		close(done)
	}()
	select {
	case <-handler.canceled:
	case <-time.After(time.Second):
		t.Fatal("in-flight handler context was not canceled")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop did not wait for handler exit")
	}
}

// TestCleanupDedupLocked_ExpiredRemoved 过期消息 ID 被清理。
func TestCleanupDedupLocked_ExpiredRemoved(t *testing.T) {
	// acc、cleanup 用于本次流程后续判断的acc、cleanup
	acc, _, _, cleanup := newAccountForTest(t)
	defer cleanup()
	defer acc.Stop()

	// now 用于本次流程后续判断的now
	now := time.Now()
	acc.dedupMu.Lock()
	// 一条过期（>1h）、一条新鲜。
	acc.processed["old-msg"] = now.Add(-2 * time.Hour)
	acc.processed["fresh-msg"] = now
	acc.dedupMu.Unlock()

	acc.dedupMu.Lock()
	acc.cleanupDedupLocked(now)
	acc.dedupMu.Unlock()

	acc.dedupMu.Lock()
	defer acc.dedupMu.Unlock()
	if // ok 用于本次流程后续判断的ok
	_, ok := acc.processed["old-msg"]; ok {
		t.Error("过期消息应被清理")
	}
	if // ok 用于本次流程后续判断的ok
	_, ok := acc.processed["fresh-msg"]; !ok {
		t.Error("新鲜消息不应被清理")
	}
}

// TestCleanupDedupLocked_OverLimitDropsOldestHalf 超上限且都未过期时删最旧一半。
func TestCleanupDedupLocked_OverLimitDropsOldestHalf(t *testing.T) {
	// acc、cleanup 用于本次流程后续判断的acc、cleanup
	acc, _, _, cleanup := newAccountForTest(t)
	defer cleanup()
	defer acc.Stop()

	// now 用于本次流程后续判断的now
	now := time.Now()
	acc.dedupMu.Lock()
	// 填入 ProcessedIDsMaxSize + 10 条新鲜消息，时间递增。
	total := ProcessedIDsMaxSize + 10
	for // i 用于本次流程后续判断的i
	i := 0; i < total; i++ {
		// 时间按 i 递增，确保有明确的最旧一半。
		acc.processed["msg-"+itoa(i)] = now.Add(time.Duration(i) * time.Millisecond)
	}
	acc.cleanupDedupLocked(now)
	// remaining 用于本次流程后续判断的remaining
	remaining := len(acc.processed)
	acc.dedupMu.Unlock()

	// 删最旧一半后：total - total/2 = (ProcessedIDsMaxSize+10) - (ProcessedIDsMaxSize+10)/2。
	want := total - total/2
	if remaining != want {
		t.Errorf("超上限删半后剩余 %d，want %d", remaining, want)
	}
}

// itoa 简单整数转字符串，避免引入 strconv。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	// neg 用于本次流程后续判断的neg
	neg := n < 0
	if neg {
		n = -n
	}
	// b 用于本次流程后续判断的b
	var b [20]byte
	// i 用于本次流程后续判断的i
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// TestCleanupDedupLocked_TriggeredByMarkAndCheck markAndCheckDedup 在超上限时触发清理。
func TestCleanupDedupLocked_TriggeredByMarkAndCheck(t *testing.T) {
	// acc、cleanup 用于本次流程后续判断的acc、cleanup
	acc, _, _, cleanup := newAccountForTest(t)
	defer cleanup()
	defer acc.Stop()

	// now 用于本次流程后续判断的now
	now := time.Now()
	acc.dedupMu.Lock()
	// 预填 ProcessedIDsMaxSize 条过期消息，使下一次 markAndCheckDedup 触发清理。
	for i := 0; i < ProcessedIDsMaxSize; i++ {
		acc.processed["old-"+itoa(i)] = now.Add(-2 * time.Hour)
	}
	acc.dedupMu.Unlock()

	// 构造一条新消息触发清理。msgID 由 extractMessageID 提取，这里直接用现成样本。
	decrypted := plainChatMessage(t, "trigger", "b1", "c1")
	// chat 用于本次流程后续判断的聊天
	chat := extractChatMessage(decrypted, "cid", "cookie")
	if !acc.markAndCheckDedup(decrypted, chat) {
		t.Fatal("新消息应允许处理")
	}

	acc.dedupMu.Lock()
	// remaining 用于本次流程后续判断的remaining
	remaining := len(acc.processed)
	acc.dedupMu.Unlock()
	// 过期消息应被清理，仅剩新消息 1 条（或新消息+残留，但应远小于 ProcessedIDsMaxSize）。
	if remaining >= ProcessedIDsMaxSize {
		t.Errorf("清理应大幅缩减 processed，剩 %d", remaining)
	}
}

// plainChatMessage 构造一条普通聊天消息（非系统卡片、contentType 非 14/26），
// 含 bizTag.messageId 以便去重提取。
// plainChatMessage 封装plain聊天消息业务协调。
func plainChatMessage(t *testing.T, text, senderUserID, chatID string) map[string]any {
	t.Helper()
	// s 用于本次流程后续判断的s
	s := `{"1":{"2":"` + chatID + `@goofish","10":{"bizTag":"{\"messageId\":\"msg-` + chatID + `\"}","reminderContent":"` + text + `","senderUserId":"` + senderUserID + `","senderNick":"买家","reminderUrl":"fleamarket://message_chat?itemId=item-` + chatID + `&peerUserId=` + senderUserID + `"}}}`
	// m 用于本次流程后续判断的m
	var m map[string]any
	if // err 用于本次流程后续判断的err
	err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// TestExtractMessageReadEvent40103 验证紧凑和批量 40103 已读事件均能生成规范回执。
func TestExtractMessageReadEvent40103(t *testing.T) {
	// event 是单条紧凑回执的解析结果；ok 表示该输入已被识别为已读事件。
	event, ok := extractMessageReadEvent(map[string]any{
		"1": "4263141580162.PNM", "2": 2, "3": 0,
		"4": "64725235816@goofish", "5": 1, "6": 1786945729928,
	})
	if !ok || event.MessageID != "4263141580162.PNM" || event.ChatID != "64725235816" || event.ReadAt <= 0 {
		t.Fatalf("40103 event=%+v ok=%v", event, ok)
	}
	// batch 是批量编码回执的解析结果；ok 复用为该回执的事件识别结果。
	batch, ok := extractMessageReadEvent(map[string]any{
		"1": []any{"4263107993838.PNM"}, "2": 2, "3": "64725235816@goofish", "4": 1,
	})
	if !ok || batch.MessageID != "4263107993838.PNM" || batch.ChatID != "64725235816" {
		t.Fatalf("40103 batch event=%+v ok=%v", batch, ok)
	}
}
