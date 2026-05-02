package master

import (
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/master/assistantcap"
)

// criticalEventTimeout 是关键事件异步重试的最长等待时间。
// HITL 事件（如 input_request）需要可靠送达，30 秒容忍网络波动和浏览器后台。
// 后台 goroutine 在此时间内仍未送达则放弃，以防止 goroutine 泄漏。
const criticalEventTimeout = 30 * time.Second

// subscriberBufferSize 是每个订阅者通道的缓冲容量。
// 从 32 提升到 256，为突发事件提供更大的缓冲余量，降低丢弃概率。
const subscriberBufferSize = 256

// deadSubscriberThreshold 是连续丢弃次数的阈值。
// 超过此阈值的订阅者将被标记为"死"订阅者，可通过 PruneDeadSubscribers 清理。
const deadSubscriberThreshold = 10

// assertNoAssistantPayload 是 P0-A structural lock 的 sink-side runtime guard。
//
// 任何走 EventBus.Broadcast 的 payload 若携带 role:"assistant" 必须经过
// Master.broadcastAssistant + assistantcap.Capability 这条唯一路径。
// 直接构造 BroadcastMessage{Payload: {"role":"assistant"...}} 并调 Broadcast
// 是 forbidden，运行时立即 panic，让单 session 崩溃换全局可见性。
//
// 覆盖 payload 类型：
//   - map[string]any / map[string]interface{}（绝大多数 chat broadcast 用此形）
//   - map[string]string（少数事件用此形）
//
// 不覆盖 struct 形 payload（AgentProgressEvent / JournalEvent 等），
// 这些不写 role 字段，由 AST 检测器作为 defense-in-depth 兜底。
func assertNoAssistantPayload(payload any) {
	const lockMsg = "[P0-A structural lock] assistant broadcasts must use Master.broadcastAssistant + assistantcap.Capability — direct EventBus.Broadcast with payload role:\"assistant\" is forbidden"
	switch p := payload.(type) {
	case map[string]any:
		if r, _ := p["role"].(string); r == "assistant" {
			panic(lockMsg)
		}
	case map[string]string:
		if p["role"] == "assistant" {
			panic(lockMsg)
		}
	}
}

// isCriticalEvent 判断消息类型是否属于关键事件。
// 关键事件不允许静默丢弃：丢失会导致 HITL 流程无法推进。
func isCriticalEvent(msgType string) bool {
	switch msgType {
	case EventTypeInputRequest, // HITL 输入请求
		EventTypeInputResponse,   // HITL 输入响应（EmitInputRequest 订阅依赖）
		"approval_request",       // 审批请求（为未来扩展保留）
		EventTypeError,           // 错误通知
		EventTypeAgentStatus,     // Agent 状态变更（completed/error 丢失会导致前端卡在"思考中"）
		EventTypePlanModeChanged: // Plan mode 切换改变用户可见阶段，不能静默丢失
		return true
	default:
		return false
	}
}

// EventBus 管理 WebSocket 广播订阅和消息分发。
//
// 生命周期：
//   - NewEventBus 创建 → Broadcast/Subscribe 正常使用 → Close 优雅关停。
//   - Close 后所有 Broadcast 和 Subscribe 操作被静默忽略，不会 panic。
//   - Close 会等待所有后台重试 goroutine 结束，确保 logger 不被悬空使用。
//
// 分发策略：
//   - Broadcast 始终非阻塞：对所有事件先尝试非阻塞写入通道。
//   - 非关键事件：通道满时直接丢弃并统计。
//   - 关键事件：通道满时启动独立后台 goroutine 异步重试（最多等待 criticalEventTimeout），
//     Broadcast 本身立即返回，RLock 迅速释放，不再有 N×5s 串行阻塞。
//
// 死订阅者清理：
//   - 每次非阻塞发送失败（通道满）都会递增该订阅者的连续丢弃计数器。
//   - 发送成功时重置计数器。
//   - 连续丢弃超过 deadSubscriberThreshold 次时标记为死订阅者。
//   - 调用 PruneDeadSubscribers 可将死订阅者从 map 中清除。
//
// 锁层次（严格有序，防止死锁）：
//
//	mu（订阅者 map） → dropsMu（丢弃计数 map）
type EventBus struct {
	subs       map[uint64]chan BroadcastMessage // 订阅者通道
	subCounter uint64                           // 订阅者 ID 计数器（原子操作）
	mu         sync.RWMutex                     // 保护 subs map

	// consecutiveDrops 记录每个订阅者的连续丢弃次数，用于死订阅者检测。
	// Broadcast 持有 mu.RLock 期间并发写此 map，故需独立锁保护。
	consecutiveDrops map[uint64]int
	dropsMu          sync.Mutex // 专门保护 consecutiveDrops

	logger *zap.Logger

	// droppedTotal 累计丢弃数量（含关键事件异步重试最终失败的情况）
	droppedTotal atomic.Int64

	// onDrop 消息丢弃回调（可选，用于可观测性埋点）
	onDrop func(msgType string, total int64)

	// retryWg 追踪所有后台 retryCriticalSend goroutine，
	// 供 Close/WaitRetries 等待全部完成。
	retryWg sync.WaitGroup

	// closeCh 在 Close() 时关闭，通知所有 retryCriticalSend goroutine 立即退出。
	// 这比等待 criticalEventTimeout 自然超时快得多，实现亚毫秒级关停。
	closeCh chan struct{}

	// closed 标记 EventBus 是否已关闭（原子操作，无锁快速路径检查）。
	closed atomic.Bool

	// closeOnce 保证 Close 只执行一次。
	closeOnce sync.Once
}

// NewEventBus 创建新的事件总线。
// 调用方在不再需要时必须调用 Close() 以释放后台 goroutine 资源。
func NewEventBus(logger *zap.Logger) *EventBus {
	return &EventBus{
		subs:             make(map[uint64]chan BroadcastMessage),
		consecutiveDrops: make(map[uint64]int),
		closeCh:          make(chan struct{}),
		logger:           logger,
	}
}

// SetOnDrop 设置消息丢弃回调（用于可观测性埋点）。
// 回调参数：msgType 为被丢弃的消息类型，total 为累计丢弃总数。
func (eb *EventBus) SetOnDrop(fn func(msgType string, total int64)) {
	eb.onDrop = fn
}

// Subscribe 创建一个 WebSocket 广播订阅，返回订阅 ID 和消息通道。
// 通道容量为 subscriberBufferSize，以减少正常负载下的丢弃可能。
// 若 EventBus 已关闭，返回 (0, nil)。
func (eb *EventBus) Subscribe() (uint64, chan BroadcastMessage) {
	if eb.closed.Load() {
		return 0, nil
	}

	ch := make(chan BroadcastMessage, subscriberBufferSize)
	subID := atomic.AddUint64(&eb.subCounter, 1)

	eb.mu.Lock()
	eb.subs[subID] = ch
	eb.mu.Unlock()

	eb.dropsMu.Lock()
	eb.consecutiveDrops[subID] = 0
	eb.dropsMu.Unlock()

	eb.logger.Debug("WebSocket 订阅已创建", zap.Uint64("sub_id", subID))
	return subID, ch
}

// Unsubscribe 取消 WebSocket 广播订阅并关闭对应通道。
func (eb *EventBus) Unsubscribe(subID uint64) {
	eb.mu.Lock()
	if ch, exists := eb.subs[subID]; exists {
		close(ch)
		delete(eb.subs, subID)
	}
	eb.mu.Unlock()

	eb.dropsMu.Lock()
	delete(eb.consecutiveDrops, subID)
	eb.dropsMu.Unlock()

	eb.logger.Debug("WebSocket 订阅已取消", zap.Uint64("sub_id", subID))
}

// Broadcast 向所有订阅者广播消息。
//
// 核心修复：Broadcast 全程持有 RLock，但对每个订阅者的写操作只做非阻塞 select。
//   - 通道有空间：直接写入，立即返回。
//   - 通道已满 + 关键事件：启动后台 goroutine 异步重试，Broadcast 不等待。
//   - 通道已满 + 非关键事件：丢弃并统计。
//
// 这样无论订阅者多慢，Broadcast 都在 O(N) 次非阻塞操作后返回，
// 彻底消除了原来最坏 5*N 秒的串行阻塞。
func (eb *EventBus) Broadcast(msg BroadcastMessage) {
	// P0-A structural lock: 任何 payload 携带 role:"assistant" 必须走
	// Master.broadcastAssistant + assistantcap.Capability。直接调本函数
	// 写入 assistant payload 是 forbidden。覆盖 map[string]any + map[string]string。
	assertNoAssistantPayload(msg.Payload)
	eb.broadcastInternal(msg)
}

// broadcastWithAssistantCap 是 capability-gated 的 assistant payload broadcast 入口。
// 跳过 sink-side assertNoAssistantPayload —— Capability 已是颁发期 gate-pass 证明，
// 二次校验只会让自己合法路径自咬。caller 必须从 assistantcap.Grant* 拿到 cap。
//
// unexported method：仅 master 包内可见，外部包拿不到。AST 规则限定包内仅
// Master.broadcastAssistant 调本方法（rule 4，待补）。
func (eb *EventBus) broadcastWithAssistantCap(_ assistantcap.Capability, msg BroadcastMessage) {
	eb.broadcastInternal(msg)
}

// broadcastInternal 是分发原 body，不做任何 payload 形态校验。
// 不可在 master 包外直接持有调用 —— 它不是公开 API。
func (eb *EventBus) broadcastInternal(msg BroadcastMessage) {
	if eb.closed.Load() {
		return
	}

	eb.mu.RLock()
	defer eb.mu.RUnlock()

	if len(eb.subs) == 0 {
		return
	}

	eb.logger.Debug("广播消息到 WebSocket 订阅者",
		zap.String("type", msg.Type),
		zap.Int("subscriber_count", len(eb.subs)),
	)

	critical := isCriticalEvent(msg.Type)

	for subID, ch := range eb.subs {
		select {
		case ch <- msg:
			// 非阻塞写入成功，重置连续丢弃计数器
			eb.dropsMu.Lock()
			eb.consecutiveDrops[subID] = 0
			eb.dropsMu.Unlock()
		default:
			// 通道已满
			if critical {
				// 关键事件：后台异步重试，不阻塞 Broadcast 主路径
				eb.retryWg.Add(1)
				go func(sid uint64, c chan BroadcastMessage, m BroadcastMessage) {
					defer eb.retryWg.Done()
					eb.retryCriticalSend(sid, c, m)
				}(subID, ch, msg)
			} else {
				// 非关键事件：直接丢弃，并更新连续丢弃计数
				eb.dropsMu.Lock()
				drops := eb.consecutiveDrops[subID] + 1
				eb.consecutiveDrops[subID] = drops
				eb.dropsMu.Unlock()

				total := eb.droppedTotal.Add(1)
				if eb.onDrop != nil {
					eb.onDrop(msg.Type, total)
				}
				eb.logger.Warn("WebSocket 订阅者通道已满，丢弃非关键消息",
					zap.Uint64("sub_id", subID),
					zap.String("msg_type", msg.Type),
					zap.Int("consecutive_drops", drops),
					zap.Int64("total_dropped", total),
				)
			}
		}
	}
}

// retryCriticalSend 在独立 goroutine 中异步重试发送关键事件。
//
// 它在 criticalEventTimeout 内持续尝试将消息写入订阅者通道：
//   - 成功：记录 Debug 日志。
//   - 超时：记录 Warn 日志并计入 droppedTotal，表明该订阅者消费严重滞后。
//
// 注意：此函数运行时不持有任何锁，通道生命周期由 Unsubscribe 负责关闭。
// 向已关闭通道发送会 panic，故使用 recover 捕获。
func (eb *EventBus) retryCriticalSend(subID uint64, ch chan BroadcastMessage, msg BroadcastMessage) {
	// 捕获向已关闭通道写入时的 panic（订阅者在重试期间取消订阅）
	defer func() {
		if r := recover(); r != nil {
			eb.logger.Debug("关键事件重试时订阅者已取消，忽略",
				zap.Uint64("sub_id", subID),
				zap.String("msg_type", msg.Type),
			)
		}
	}()

	timer := time.NewTimer(criticalEventTimeout)
	defer timer.Stop()

	select {
	case ch <- msg:
		// 重试成功
		eb.logger.Debug("关键事件异步重试成功",
			zap.Uint64("sub_id", subID),
			zap.String("msg_type", msg.Type),
		)
	case <-timer.C:
		// 超时仍未送达，订阅者严重滞后
		total := eb.droppedTotal.Add(1)
		if eb.onDrop != nil {
			eb.onDrop(msg.Type, total)
		}
		eb.logger.Warn("关键事件重试超时，订阅者消费严重滞后",
			zap.Uint64("sub_id", subID),
			zap.String("msg_type", msg.Type),
			zap.Duration("timeout", criticalEventTimeout),
			zap.Int64("total_dropped", total),
		)
	case <-eb.closeCh:
		// EventBus 正在关停，立即退出，不再等待 criticalEventTimeout
		eb.logger.Debug("EventBus 关停，中止关键事件重试",
			zap.Uint64("sub_id", subID),
			zap.String("msg_type", msg.Type),
		)
	}
}

// PruneDeadSubscribers 清理连续丢弃次数超过阈值的死订阅者。
//
// 死订阅者通常是已断开连接但未调用 Unsubscribe 的客户端。
// 此方法会关闭其通道并从 map 中删除，防止后续 Broadcast 持续为其启动重试 goroutine。
//
// 建议由定时任务或健康检查周期性调用（如每分钟一次）。
// 返回被清理的订阅者 ID 列表，方便调用方记录日志。
func (eb *EventBus) PruneDeadSubscribers() []uint64 {
	// 第一步：在 dropsMu 下收集死订阅者 ID
	eb.dropsMu.Lock()
	var dead []uint64
	for subID, drops := range eb.consecutiveDrops {
		if drops >= deadSubscriberThreshold {
			dead = append(dead, subID)
		}
	}
	eb.dropsMu.Unlock()

	if len(dead) == 0 {
		return nil
	}

	// 第二步：在 mu.Lock + dropsMu 下删除死订阅者（严格按锁层次顺序获取）
	eb.mu.Lock()
	eb.dropsMu.Lock()
	pruned := dead[:0] // 复用切片，只保留真正被删除的
	for _, subID := range dead {
		// 二次检查：可能已被 Unsubscribe 删除
		if ch, exists := eb.subs[subID]; exists {
			close(ch)
			delete(eb.subs, subID)
			delete(eb.consecutiveDrops, subID)
			pruned = append(pruned, subID)
		}
	}
	eb.dropsMu.Unlock()
	eb.mu.Unlock()

	if len(pruned) > 0 {
		eb.logger.Warn("已清理死订阅者",
			zap.Int("count", len(pruned)),
			zap.Uint64s("sub_ids", pruned),
		)
	}
	return pruned
}

// DroppedTotal 返回累计丢弃的消息数量（含关键事件最终放弃的情况）。
func (eb *EventBus) DroppedTotal() int64 {
	return eb.droppedTotal.Load()
}

// Close 优雅关停 EventBus：
//  1. 标记 closed 状态，后续 Broadcast/Subscribe 调用被静默忽略。
//  2. 关闭 closeCh，通知所有正在等待的 retryCriticalSend goroutine 立即退出
//     （无需等待 criticalEventTimeout 自然超时，实现亚毫秒级关停）。
//  3. 等待所有后台 retry goroutine 完成（retryWg.Wait），确保 logger 不被悬空使用。
//  4. 关闭所有订阅者通道并清空 map，释放资源。
//
// Close 是幂等的，可安全多次调用。
// 在生产环境中由 Master.Stop() 调用；在测试中通过 t.Cleanup 调用。
func (eb *EventBus) Close() {
	eb.closeOnce.Do(func() {
		// 步骤 1：标记关闭，阻止新的 Broadcast/Subscribe
		eb.closed.Store(true)

		// 步骤 2：通知所有 retryCriticalSend goroutine 立即退出
		close(eb.closeCh)

		// 步骤 3：等待所有后台 retry goroutine 结束
		eb.retryWg.Wait()

		// 步骤 4：关闭所有订阅者通道，释放资源
		eb.mu.Lock()
		for subID, ch := range eb.subs {
			close(ch)
			delete(eb.subs, subID)
		}
		eb.mu.Unlock()

		eb.dropsMu.Lock()
		for subID := range eb.consecutiveDrops {
			delete(eb.consecutiveDrops, subID)
		}
		eb.dropsMu.Unlock()

		eb.logger.Debug("EventBus 已关闭")
	})
}

// WaitRetries 阻塞等待所有后台 retryCriticalSend goroutine 完成。
// 已废弃：优先使用 Close()，它在等待 goroutine 的同时还会清理所有资源。
func (eb *EventBus) WaitRetries() {
	eb.retryWg.Wait()
}

// BroadcastInputRequest 广播 input_request 消息（关键事件，异步重试保障）。
// 将 req.SessionID 填充到 BroadcastMessage.SessionID，供前端过滤使用。
func (eb *EventBus) BroadcastInputRequest(req *InputRequest) {
	eb.Broadcast(BroadcastMessage{
		Type:      EventTypeInputRequest,
		Payload:   req,
		SessionID: req.SessionID,
	})
}

// BroadcastInputResponse 广播 input_response 消息（关键事件）。
// EmitInputRequest 路径下的 Subscribe 方通过 reqID 过滤收取。
func (eb *EventBus) BroadcastInputResponse(resp *InputResponse) {
	eb.Broadcast(BroadcastMessage{
		Type:    EventTypeInputResponse,
		Payload: resp,
	})
}

// BroadcastGenericMessage 广播通用消息。
func (eb *EventBus) BroadcastGenericMessage(msgType string, payload interface{}) {
	eb.Broadcast(BroadcastMessage{
		Type:    msgType,
		Payload: payload,
	})
}

// BroadcastSessionMessage 广播与特定会话关联的消息。
// 与 BroadcastGenericMessage 不同，此方法会填充 SessionID 字段。
func (eb *EventBus) BroadcastSessionMessage(sessionID string, msg BroadcastMessage) {
	msg.SessionID = sessionID
	eb.Broadcast(msg)
}
