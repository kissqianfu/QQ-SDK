package apierr

// apierr_test.go 锁定错误码判定边界（对照官方错误码表）。
// 之前 apierr 包 0% 覆盖，判定方法写错也没人发现。

import (
	"net/http"
	"testing"
)

func TestIsDuplicate(t *testing.T) {
	if !(&APIError{Code: 40054005}).IsDuplicate() {
		t.Fatal("40054005 应判为重复")
	}
	if (&APIError{Code: 40054006}).IsDuplicate() {
		t.Fatal("40054006 不应判为重复")
	}
}

func TestIsExpired(t *testing.T) {
	for _, code := range []int{40034128, 304103, 40034005, 40034026, 40034024, 40034025, 40034027} {
		if !(&APIError{Code: code}).IsExpired() {
			t.Errorf("%d 应判为过期/锚点不可用", code)
		}
	}
	if (&APIError{Code: 40034006}).IsExpired() {
		t.Fatal("内容违规不应判为过期")
	}
}

func TestIsAuth(t *testing.T) {
	if !(&APIError{HTTP: http.StatusUnauthorized}).IsAuth() {
		t.Fatal("HTTP 401 应判为鉴权失败")
	}
	for _, code := range []int{11241, 11243, 11253, 11282, 304004, 304036, 40034105, 40034127} {
		if !(&APIError{Code: code}).IsAuth() {
			t.Errorf("%d 应判为鉴权/权限错误", code)
		}
	}
	if (&APIError{Code: 40034005}).IsAuth() {
		t.Fatal("被动回复过期不应判为鉴权")
	}
}

func TestIsRateLimited(t *testing.T) {
	if !(&APIError{Code: 40034100}).IsRateLimited() {
		t.Fatal("40034100 应判为频控")
	}
	if !(&APIError{Code: 50002}).IsRateLimited() {
		t.Fatal("50002 应判为流式频控")
	}
	if !(&APIError{HTTP: http.StatusTooManyRequests}).IsRateLimited() {
		t.Fatal("HTTP 429 应判为频控")
	}
	if (&APIError{Code: 40054007}).IsRateLimited() {
		t.Fatal("消息超长不应判为频控")
	}
}

func TestThrottleError(t *testing.T) {
	err := &ThrottleError{Layer: "group"}
	if !err.Is(ErrThrottledLocal) {
		t.Fatal("ThrottleError 不匹配 ErrThrottledLocal")
	}
	if got := err.Error(); got == "" {
		t.Fatal("Error() 为空")
	}
}
