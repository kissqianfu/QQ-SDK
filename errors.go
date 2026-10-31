package qqbot

import "github.com/kissqianfu/QQ-SDK/apierr"

// APIError 是平台错误；实现与判定方法在 apierr 包（根包与 api 包共用一份）。
type APIError = apierr.APIError
