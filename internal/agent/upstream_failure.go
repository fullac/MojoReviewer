// 将上游固定格式错误转为可公开的分类，避免泄露响应正文并停止永久错误的后续批次。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/mojoreviewer/mojoreviewer/internal/review"
)

var upstreamStatusPattern = regexp.MustCompile(`^API error ([45][0-9]{2}):\s*`)
var modelLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

type upstreamFailure struct {
	status int
	reason string
	cause  error
}

func (e *upstreamFailure) Error() string { return e.reason }
func (e *upstreamFailure) Unwrap() error { return e.cause }

func safeModelLabel(model string, secrets ...string) string {
	if !modelLabelPattern.MatchString(model) || review.ValidateComment(model, secrets...) != nil {
		return "已隐藏"
	}
	return model
}

func classifyUpstream(err error, model string) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var existing *upstreamFailure
	if errors.As(err, &existing) {
		return err
	}
	// 只认 SDK 产生的稳定前缀，不在任意正文中搜索状态码或错误类型。
	cause := err
	for errors.Unwrap(cause) != nil {
		cause = errors.Unwrap(cause)
	}
	text := cause.Error()
	match := upstreamStatusPattern.FindStringSubmatchIndex(text)
	if match == nil {
		return err
	}
	status, _ := strconv.Atoi(text[match[2]:match[3]])
	kind := "upstream_error"
	body := text[match[1]:]
	if len(body) <= 16<<10 {
		var envelope struct {
			Error struct {
				Type string `json:"type"`
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(body), &envelope) == nil {
			for _, candidate := range []string{envelope.Error.Type, envelope.Error.Code} {
				switch candidate {
				case "model_not_found", "invalid_api_key", "authentication_error", "permission_error", "rate_limit_error", "insufficient_quota", "invalid_request_error":
					kind = candidate
				}
				if kind != "upstream_error" {
					break
				}
			}
		}
	}
	action := "请检查模型服务状态。"
	switch status {
	case 401:
		action = "请检查模型服务凭据。"
	case 403:
		action = "请检查模型服务权限或分组。"
	case 404:
		action = "请检查模型名称及服务地址或分组是否支持该模型。"
	case 429:
		action = "模型服务限流或额度不足，请稍后重试或检查额度。"
	}
	if kind == "insufficient_quota" {
		action = "模型服务额度不足，请检查额度。"
	}
	action += "处理后请显式请求重新审查。"
	reason := fmt.Sprintf("模型服务请求失败（HTTP %d，type=%s，model=%s），审查未完成。%s", status, kind, safeModelLabel(model), action)
	return &upstreamFailure{status: status, reason: reason, cause: err}
}

func permanentUpstreamFailure(err error) bool {
	var failure *upstreamFailure
	if !errors.As(err, &failure) {
		return false
	}
	return failure.status == 400 || failure.status == 401 || failure.status == 403 || failure.status == 404
}

// upstreamReason 不返回上游的 message 或响应正文。
func upstreamReason(err error) string {
	var failure *upstreamFailure
	if errors.As(err, &failure) {
		return strings.TrimSpace(failure.reason)
	}
	return ""
}
