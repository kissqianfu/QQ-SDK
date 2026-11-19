package qqbot

import "github.com/kissqianfu/QQ-SDK/common/apierr"

// APIError 是平台错误；实现与判定方法在 apierr 包（api / qqbot 共用一份）。
type APIError = apierr.APIError
