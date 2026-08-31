package completionruntime

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"ds2api/internal/assistantturn"
	"ds2api/internal/auth"
	"ds2api/internal/config"
	dsclient "ds2api/internal/deepseek/client"
	"ds2api/internal/httpapi/openai/history"
	"ds2api/internal/httpapi/openai/shared"
	"ds2api/internal/promptcompat"
	"ds2api/internal/sse"
)

type DeepSeekCaller interface {
	CreateSession(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error)
	GetPow(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error)
	UploadFile(ctx context.Context, a *auth.RequestAuth, req dsclient.UploadFileRequest, maxAttempts int) (*dsclient.UploadFileResult, error)
	CallCompletion(ctx context.Context, a *auth.RequestAuth, payload map[string]any, powResp string, maxAttempts int) (*http.Response, error)
	StopStream(ctx context.Context, a *auth.RequestAuth, sessionID string, messageID int) error
	FireCompletionAndStop(ctx context.Context, a *auth.RequestAuth, payload map[string]any, powResp string) (int, error)
}

type Options struct {
	StripReferenceMarkers bool
	MaxAttempts           int
	RetryEnabled          bool
	RetryMaxAttempts      int
	CurrentInputFile      history.CurrentInputConfigReader
	ExpertPromptSegment   ExpertPromptSegmentConfigReader
}

type NonStreamResult struct {
	SessionID string
	Payload   map[string]any
	Turn      assistantturn.Turn
	Attempts  int
}

type StartResult struct {
	SessionID string
	Payload   map[string]any
	Pow       string
	Response  *http.Response
	Request   promptcompat.StandardRequest
}

func StartCompletion(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, opts Options) (StartResult, *assistantturn.OutputError) {
	if segments := shouldSegmentExpertPrompt(stdReq, opts); segments != nil {
		return StartCompletionWithSegments(ctx, ds, a, stdReq, opts, segments)
	}
	return startCompletionOnce(ctx, ds, a, stdReq, opts)
}

// PrepareCompletionPayload builds the session-aware completion payload for a
// request without calling the completion endpoint itself. Oversized expert
// prompts are split into segments first (all but the last are sent via
// FireCompletionAndStop), so the caller can stream the returned final payload
// directly. This is the payload-side equivalent of StartCompletion for
// surfaces that stream upstream responses themselves (e.g. the Vercel Node
// stream layer). On success the caller must stream the returned payload with
// the returned PoW on the same account and session.
func PrepareCompletionPayload(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, opts Options, maxAttempts int) (sessionID, pow string, payload map[string]any, outErr *assistantturn.OutputError) {
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	var prepErr *assistantturn.OutputError
	stdReq, prepErr = prepareCurrentInputFile(ctx, ds, a, stdReq, opts)
	if prepErr != nil {
		return "", "", nil, prepErr
	}
	var err error
	sessionID, err = ds.CreateSession(ctx, a, maxAttempts)
	if err != nil {
		return "", "", nil, authOutputError(a, err)
	}
	if segments := shouldSegmentExpertPrompt(stdReq, opts); len(segments) > 1 {
		finalPow, finalPayload, segErr := fireSegmentPayloads(ctx, ds, a, stdReq, sessionID, segments, maxAttempts)
		if segErr != nil {
			return sessionID, "", nil, segErr
		}
		return sessionID, finalPow, finalPayload, nil
	}
	pow, err = ds.GetPow(ctx, a, maxAttempts)
	if err != nil {
		if blockedErr := blockedUpstreamError(err); blockedErr != nil {
			return sessionID, "", nil, blockedErr
		}
		return sessionID, "", nil, &assistantturn.OutputError{Status: http.StatusUnauthorized, Message: "Failed to get PoW (invalid token or unknown error).", Code: "error"}
	}
	return sessionID, pow, stdReq.CompletionPayload(sessionID), nil
}

func startCompletionOnce(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, opts Options) (StartResult, *assistantturn.OutputError) {
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
	pow, err := ds.GetPow(ctx, a, maxAttempts)
	if err != nil {
		if blockedErr := blockedUpstreamError(err); blockedErr != nil {
			return StartResult{SessionID: sessionID, Request: stdReq}, blockedErr
		}
		return StartResult{SessionID: sessionID, Request: stdReq}, &assistantturn.OutputError{Status: http.StatusUnauthorized, Message: "Failed to get PoW (invalid token or unknown error).", Code: "error"}
	}
	payload := stdReq.CompletionPayload(sessionID)
	resp, err := ds.CallCompletion(ctx, a, payload, pow, maxAttempts)
	if err != nil {
		if blockedErr := blockedUpstreamError(err); blockedErr != nil {
			return StartResult{SessionID: sessionID, Payload: payload, Pow: pow, Request: stdReq}, blockedErr
		}
		if dsclient.IsMutedError(err) {
			return StartResult{SessionID: sessionID, Payload: payload, Pow: pow, Request: stdReq}, &assistantturn.OutputError{Status: http.StatusForbidden, Message: "Account is muted by upstream.", Code: "account_muted"}
		}
		return StartResult{SessionID: sessionID, Payload: payload, Pow: pow, Request: stdReq}, &assistantturn.OutputError{Status: http.StatusInternalServerError, Message: "Failed to get completion.", Code: "error"}
	}
	return StartResult{SessionID: sessionID, Payload: payload, Pow: pow, Response: resp, Request: stdReq}, nil
}

func prepareCurrentInputFile(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, opts Options) (promptcompat.StandardRequest, *assistantturn.OutputError) {
	if opts.CurrentInputFile == nil || stdReq.CurrentInputFileApplied {
		return stdReq, nil
	}
	out, err := (history.Service{Store: opts.CurrentInputFile, DS: ds}).ApplyCurrentInputFile(ctx, a, stdReq)
	if err != nil {
		status, message := history.MapError(err)
		return out, &assistantturn.OutputError{Status: status, Message: message, Code: "error"}
	}
	return out, nil
}

func ExecuteNonStreamWithRetry(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, opts Options) (NonStreamResult, *assistantturn.OutputError) {
	start, startErr := StartCompletion(ctx, ds, a, stdReq, opts)
	if startErr != nil {
		return NonStreamResult{SessionID: start.SessionID, Payload: start.Payload}, startErr
	}
	return ExecuteNonStreamStartedWithRetry(ctx, ds, a, start, opts)
}

func ExecuteNonStreamStartedWithRetry(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, start StartResult, opts Options) (NonStreamResult, *assistantturn.OutputError) {
	stdReq := start.Request
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	sessionID := start.SessionID
	payload := start.Payload
	pow := start.Pow

	attempts := 0
	accountSwitchAttempted := false
	currentResp := start.Response
	usagePrompt := stdReq.PromptTokenText
	accumulatedThinking := ""
	accumulatedRawThinking := ""
	accumulatedToolDetectionThinking := ""
	for {
		turn, outErr := collectAttempt(currentResp, stdReq, usagePrompt, opts)
		if outErr != nil {
			if canRetryOnAlternateAccount(ctx, a, outErr, opts.RetryEnabled, &accountSwitchAttempted) {
				switched, switchErr := startStandardCompletionOnAlternateAccount(ctx, ds, a, stdReq, opts, maxAttempts)
				if switchErr != nil {
					return NonStreamResult{SessionID: sessionID, Payload: payload, Attempts: attempts}, switchErr
				}
				if switched.Response != nil {
					config.Logger.Info("[completion_runtime_account_switch_retry] retrying after 429", "surface", stdReq.Surface, "stream", false, "account", a.AccountID)
					sessionID = switched.SessionID
					payload = switched.Payload
					pow = switched.Pow
					currentResp = switched.Response
					usagePrompt = stdReq.PromptTokenText
					accumulatedThinking = ""
					accumulatedRawThinking = ""
					accumulatedToolDetectionThinking = ""
					continue
				}
			}
			return NonStreamResult{SessionID: sessionID, Payload: payload, Attempts: attempts}, outErr
		}
		accumulatedThinking += sse.TrimContinuationOverlap(accumulatedThinking, turn.Thinking)
		accumulatedRawThinking += sse.TrimContinuationOverlap(accumulatedRawThinking, turn.RawThinking)
		accumulatedToolDetectionThinking += sse.TrimContinuationOverlap(accumulatedToolDetectionThinking, turn.DetectionThinking)
		turn.Thinking = accumulatedThinking
		turn.RawThinking = accumulatedRawThinking
		turn.DetectionThinking = accumulatedToolDetectionThinking
		turn = assistantturn.BuildTurnFromCollected(sse.CollectResult{
			Text:                  turn.RawText,
			Thinking:              turn.RawThinking,
			ToolDetectionThinking: turn.DetectionThinking,
			ContentFilter:         turn.ContentFilter,
			CitationLinks:         turn.CitationLinks,
			ResponseMessageID:     turn.ResponseMessageID,
		}, buildOptions(stdReq, usagePrompt, opts))

		retryMax := opts.RetryMaxAttempts
		if retryMax <= 0 {
			retryMax = shared.EmptyOutputRetryMaxAttempts()
		}
		if !opts.RetryEnabled || !assistantturn.ShouldRetryEmptyOutput(turn, attempts, retryMax) {
			if canRetryOnAlternateAccount(ctx, a, turn.Error, opts.RetryEnabled, &accountSwitchAttempted) {
				switched, switchErr := startStandardCompletionOnAlternateAccount(ctx, ds, a, stdReq, opts, maxAttempts)
				if switchErr != nil {
					return NonStreamResult{SessionID: sessionID, Payload: payload, Turn: turn, Attempts: attempts}, switchErr
				}
				if switched.Response != nil {
					config.Logger.Info("[completion_runtime_account_switch_retry] retrying after 429", "surface", stdReq.Surface, "stream", false, "account", a.AccountID)
					sessionID = switched.SessionID
					payload = switched.Payload
					pow = switched.Pow
					currentResp = switched.Response
					usagePrompt = stdReq.PromptTokenText
					accumulatedThinking = ""
					accumulatedRawThinking = ""
					accumulatedToolDetectionThinking = ""
					continue
				}
			}
			return NonStreamResult{SessionID: sessionID, Payload: payload, Turn: turn, Attempts: attempts}, turn.Error
		}

		attempts++
		parentMessageID := retryParentMessageID(turn.ResponseMessageID, payload)
		config.Logger.Info("[completion_runtime_empty_retry] attempting synthetic retry", "surface", stdReq.Surface, "stream", false, "retry_attempt", attempts, "parent_message_id", parentMessageID)
		retryPow, powErr := ds.GetPow(ctx, a, maxAttempts)
		if powErr != nil {
			config.Logger.Warn("[completion_runtime_empty_retry] retry PoW fetch failed, falling back to original PoW", "surface", stdReq.Surface, "retry_attempt", attempts, "error", powErr)
			retryPow = pow
		}
		retryPayload := shared.ClonePayloadForEmptyOutputRetry(payload, parentMessageID)
		nextResp, err := ds.CallCompletion(ctx, a, retryPayload, retryPow, maxAttempts)
		if err != nil {
			if blockedErr := blockedUpstreamError(err); blockedErr != nil {
				return NonStreamResult{SessionID: sessionID, Payload: payload, Turn: turn, Attempts: attempts}, blockedErr
			}
			return NonStreamResult{SessionID: sessionID, Payload: payload, Turn: turn, Attempts: attempts}, &assistantturn.OutputError{Status: http.StatusInternalServerError, Message: "Failed to get completion.", Code: "error"}
		}
		payload = retryPayload
		usagePrompt = shared.UsagePromptWithEmptyOutputRetry(usagePrompt, attempts)
		currentResp = nextResp
	}
}

func retryParentMessageID(observed int, payload map[string]any) int {
	if observed > 0 {
		return observed
	}
	return payloadParentMessageID(payload)
}

func payloadParentMessageID(payload map[string]any) int {
	if payload == nil {
		return 0
	}
	switch v := payload["parent_message_id"].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case float32:
		return int(v)
	default:
		return 0
	}
}

func canRetryOnAlternateAccount(ctx context.Context, a *auth.RequestAuth, outErr *assistantturn.OutputError, retryEnabled bool, attempted *bool) bool {
	if outErr == nil || !retryEnabled || a == nil || !a.UseConfigToken {
		return false
	}
	if isAccountMuted(outErr) {
		return a.SwitchAccount(ctx)
	}
	if isUpstreamUnavailable(outErr) {
		return a.SwitchAccount(ctx)
	}
	if outErr.Status != http.StatusTooManyRequests {
		return false
	}
	if attempted == nil || *attempted {
		return false
	}
	*attempted = true
	return a.SwitchAccount(ctx)
}

func isUpstreamUnavailable(outErr *assistantturn.OutputError) bool {
	return outErr != nil && outErr.Code == "upstream_unavailable"
}

func isAccountMuted(outErr *assistantturn.OutputError) bool {
	return outErr != nil && outErr.Code == "account_muted"
}

func startStandardCompletionOnAlternateAccount(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, opts Options, maxAttempts int) (StartResult, *assistantturn.OutputError) {
	if segments := shouldSegmentExpertPrompt(stdReq, opts); segments != nil {
		return StartCompletionWithSegments(ctx, ds, a, stdReq, opts, segments)
	}
	var prepErr *assistantturn.OutputError
	stdReq, prepErr = reuploadCurrentInputFileForAccount(ctx, ds, a, stdReq, opts)
	if prepErr != nil {
		return StartResult{Request: stdReq}, prepErr
	}
	sessionID, err := ds.CreateSession(ctx, a, maxAttempts)
	if err != nil {
		return StartResult{}, authOutputError(a, err)
	}
	pow, err := ds.GetPow(ctx, a, maxAttempts)
	if err != nil {
		if blockedErr := blockedUpstreamError(err); blockedErr != nil {
			return StartResult{SessionID: sessionID}, blockedErr
		}
		return StartResult{SessionID: sessionID}, &assistantturn.OutputError{Status: http.StatusUnauthorized, Message: "Failed to get PoW (invalid token or unknown error).", Code: "error"}
	}
	payload := stdReq.CompletionPayload(sessionID)
	resp, err := ds.CallCompletion(ctx, a, payload, pow, maxAttempts)
	if err != nil {
		if blockedErr := blockedUpstreamError(err); blockedErr != nil {
			return StartResult{SessionID: sessionID, Payload: payload, Pow: pow}, blockedErr
		}
		if dsclient.IsMutedError(err) {
			return StartResult{SessionID: sessionID, Payload: payload, Pow: pow}, &assistantturn.OutputError{Status: http.StatusForbidden, Message: "Account is muted by upstream.", Code: "account_muted"}
		}
		return StartResult{SessionID: sessionID, Payload: payload, Pow: pow}, &assistantturn.OutputError{Status: http.StatusInternalServerError, Message: "Failed to get completion.", Code: "error"}
	}
	return StartResult{SessionID: sessionID, Payload: payload, Pow: pow, Response: resp, Request: stdReq}, nil
}

func reuploadCurrentInputFileForAccount(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, opts Options) (promptcompat.StandardRequest, *assistantturn.OutputError) {
	if opts.CurrentInputFile == nil || !stdReq.CurrentInputFileApplied {
		return stdReq, nil
	}
	out, err := (history.Service{Store: opts.CurrentInputFile, DS: ds}).ReuploadAppliedCurrentInputFile(ctx, a, stdReq)
	if err != nil {
		status, message := history.MapError(err)
		return out, &assistantturn.OutputError{Status: status, Message: message, Code: "error"}
	}
	return out, nil
}

func collectAttempt(resp *http.Response, stdReq promptcompat.StandardRequest, usagePrompt string, opts Options) (assistantturn.Turn, *assistantturn.OutputError) {
	defer func() {
		if err := resp.Body.Close(); err != nil {
			config.Logger.Warn("[completion_runtime] response body close failed", "surface", stdReq.Surface, "error", err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		if captchaBody := tryDetectCaptchaFromBody(body); captchaBody != "" {
			config.Logger.Warn("[completion_runtime] captcha challenge detected, mapping to 429 for account switch", "surface", stdReq.Surface, "detail", captchaBody)
			return assistantturn.Turn{}, &assistantturn.OutputError{Status: http.StatusTooManyRequests, Message: "Captcha challenge detected, account may be rate-limited.", Code: "captcha_required"}
		}
		return assistantturn.Turn{}, &assistantturn.OutputError{Status: resp.StatusCode, Message: message, Code: "error"}
	}
	result := sse.CollectStream(resp, stdReq.Thinking, false)
	return assistantturn.BuildTurnFromCollected(result, buildOptions(stdReq, usagePrompt, opts)), nil
}

func buildOptions(stdReq promptcompat.StandardRequest, prompt string, opts Options) assistantturn.BuildOptions {
	return assistantturn.BuildOptions{
		Model:                 stdReq.ResponseModel,
		Prompt:                prompt,
		RefFileTokens:         stdReq.RefFileTokens,
		SearchEnabled:         stdReq.Search,
		StripReferenceMarkers: opts.StripReferenceMarkers,
		ToolNames:             stdReq.ToolNames,
		ToolsRaw:              stdReq.ToolsRaw,
		ToolChoice:            stdReq.ToolChoice,
	}
}

// authOutputError 映射会话创建类失败。上游风控拦截（WAF / Cloudflare challenge）
// 拦的是出口 IP/指纹，不是账号 token：沿用 401“请重新登录”会把运维引向错误方向，
// 因此优先判断拦截并返回 502。
func authOutputError(a *auth.RequestAuth, err error) *assistantturn.OutputError {
	if blockedErr := blockedUpstreamError(err); blockedErr != nil {
		return blockedErr
	}
	if a != nil && a.UseConfigToken {
		return &assistantturn.OutputError{Status: http.StatusUnauthorized, Message: "Account token is invalid. Please re-login the account in admin.", Code: "error"}
	}
	return &assistantturn.OutputError{Status: http.StatusUnauthorized, Message: DirectTokenErrorMessage, Code: "error"}
}

const DirectTokenErrorMessage = "Invalid token. If this should be a DS2API key, add it to config.keys first."

func IsDirectTokenAuthError(outErr *assistantturn.OutputError) bool {
	return outErr != nil && outErr.Status == http.StatusUnauthorized && outErr.Message == DirectTokenErrorMessage
}

func Errorf(status int, format string, args ...any) *assistantturn.OutputError {
	return &assistantturn.OutputError{Status: status, Message: fmt.Sprintf(format, args...), Code: "error"}
}
