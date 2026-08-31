package completionruntime

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"ds2api/internal/assistantturn"
	"ds2api/internal/auth"
	"ds2api/internal/config"
	dsclient "ds2api/internal/deepseek/client"
	"ds2api/internal/promptcompat"
)

func StartCompletionWithSegments(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, opts Options, segments []string) (StartResult, *assistantturn.OutputError) {
	if len(segments) <= 1 {
		return startCompletionOnce(ctx, ds, a, stdReq, opts)
	}

	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}

	var prepErr *assistantturn.OutputError
	stdReq, prepErr = prepareCurrentInputFile(ctx, ds, a, stdReq, opts)
	if prepErr != nil {
		return StartResult{Request: stdReq}, prepErr
	}

	sessionID, err := ds.CreateSession(ctx, a, maxAttempts)
	if err != nil {
		return StartResult{Request: stdReq}, authOutputError(a, err)
	}

	finalPow, finalPayload, outErr := fireSegmentPayloads(ctx, ds, a, stdReq, sessionID, segments, maxAttempts)
	if outErr != nil {
		return StartResult{SessionID: sessionID, Request: stdReq}, outErr
	}
	resp, err := ds.CallCompletion(ctx, a, finalPayload, finalPow, maxAttempts)
	if err != nil {
		if blockedErr := blockedUpstreamError(err); blockedErr != nil {
			return StartResult{SessionID: sessionID, Payload: finalPayload, Pow: finalPow, Request: stdReq}, blockedErr
		}
		if dsclient.IsMutedError(err) {
			return StartResult{SessionID: sessionID, Payload: finalPayload, Pow: finalPow, Request: stdReq}, &assistantturn.OutputError{Status: http.StatusForbidden, Message: "Account is muted by upstream.", Code: "account_muted"}
		}
		return StartResult{SessionID: sessionID, Payload: finalPayload, Pow: finalPow, Request: stdReq}, &assistantturn.OutputError{Status: http.StatusInternalServerError, Message: "Failed to get completion.", Code: "error"}
	}
	return StartResult{SessionID: sessionID, Payload: finalPayload, Pow: finalPow, Response: resp, Request: stdReq}, nil
}

// fireSegmentPayloads sends all but the last prompt segment via
// FireCompletionAndStop and returns the PoW and payload for the final segment.
// The returned payload still carries the parent_message_id chain so the caller
// can either stream it directly or continue it with a retry payload.
//
// 当某个分段发送失败、或发送后上游未确认消息已提交（ErrSegmentCommitUnconfirmed）
// 时，parent_message_id 链已不可信：继续链式续发会让上游无法把前序分段并入
// 上下文，最终回复表现为"丢失上下文"。此时回退为单消息发送剩余全部内容
// （分段由 SplitByMaxRunes 硬切，按序拼接即还原原文），并把已成功提交的
// 最后一个 parent 作为该消息的 parent，保证最终请求携带尽可能完整的上下文。
func fireSegmentPayloads(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, sessionID string, segments []string, maxAttempts int) (string, map[string]any, *assistantturn.OutputError) {
	parentMessageID := 0
	for i := 0; i < len(segments)-1; i++ {
		segPow, err := ds.GetPow(ctx, a, maxAttempts)
		if err != nil {
			if blockedErr := blockedUpstreamError(err); blockedErr != nil {
				return "", nil, blockedErr
			}
			return "", nil, &assistantturn.OutputError{Status: http.StatusUnauthorized, Message: "Failed to get PoW (invalid token or unknown error).", Code: "error"}
		}
		segPayload := stdReq.CompletionPayloadWithParentAndPrompt(sessionID, parentMessageID, segments[i])
		logSegmentPayload("fire-stop", i, len(segments), sessionID, parentMessageID, segments[i])
		respID, err := ds.FireCompletionAndStop(ctx, a, segPayload, segPow)
		if err != nil {
			if dsclient.IsMutedError(err) {
				return "", nil, &assistantturn.OutputError{Status: http.StatusForbidden, Message: "Account is muted by upstream.", Code: "account_muted"}
			}
			fallbackPrompt := strings.Join(segments[i:], "")
			unconfirmed := errors.Is(err, dsclient.ErrSegmentCommitUnconfirmed)
			config.Logger.Warn("[expert_segment_fallback] segment send failed, falling back to single-message prompt",
				"segment_index", i, "segment_total", len(segments), "session_id", sessionID,
				"parent_message_id", parentMessageID, "commit_unconfirmed", unconfirmed, "error", err)
			logSegmentPayload("fallback", i, len(segments), sessionID, parentMessageID, fallbackPrompt)
			return finishSegmentFallback(ctx, ds, a, stdReq, sessionID, parentMessageID, fallbackPrompt, maxAttempts)
		}
		parentMessageID = respID
	}

	finalPow, err := ds.GetPow(ctx, a, maxAttempts)
	if err != nil {
		if blockedErr := blockedUpstreamError(err); blockedErr != nil {
			return "", nil, blockedErr
		}
		return "", nil, &assistantturn.OutputError{Status: http.StatusUnauthorized, Message: "Failed to get PoW (invalid token or unknown error).", Code: "error"}
	}
	finalPayload := stdReq.CompletionPayloadWithParentAndPrompt(sessionID, parentMessageID, segments[len(segments)-1])
	logSegmentPayload("final", len(segments)-1, len(segments), sessionID, parentMessageID, segments[len(segments)-1])
	return finalPow, finalPayload, nil
}

// finishSegmentFallback 构造回退路径的最终 payload：把剩余分段合并为一条
// 消息发送，parent 沿用最后一个已成功提交的分段，避免依赖断裂的链。
func finishSegmentFallback(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, sessionID string, parentMessageID int, fallbackPrompt string, maxAttempts int) (string, map[string]any, *assistantturn.OutputError) {
	finalPow, err := ds.GetPow(ctx, a, maxAttempts)
	if err != nil {
		if blockedErr := blockedUpstreamError(err); blockedErr != nil {
			return "", nil, blockedErr
		}
		return "", nil, &assistantturn.OutputError{Status: http.StatusUnauthorized, Message: "Failed to get PoW (invalid token or unknown error).", Code: "error"}
	}
	finalPayload := stdReq.CompletionPayloadWithParentAndPrompt(sessionID, parentMessageID, fallbackPrompt)
	logSegmentPayload("fallback", -1, -1, sessionID, parentMessageID, fallbackPrompt)
	return finalPow, finalPayload, nil
}

func logSegmentPayload(kind string, index int, total int, sessionID string, parentMessageID int, prompt string) {
	config.Logger.Info("[start_completion_with_segments] sending segment",
		"kind", kind,
		"segment_index", index,
		"segment_total", total,
		"session_id", sessionID,
		"parent_message_id", parentMessageID,
		"prompt_runes", len([]rune(prompt)),
	)
}
