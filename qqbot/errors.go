package qqbot

import "github.com/kissqianfu/QQ-SDK/core/apierr"

// APIError 是平台错误；实现与判定方法在 apierr 包（api / qqbot 共用一份）。
type APIError = apierr.APIError
