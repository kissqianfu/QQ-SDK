package api

// paging.go 是分页接口的公共骨架。
//
// 平台的分页接口形态一致：query 里 cursor/limit，响应里 next_cursor（空=末页）。
// 之前每个列表接口各写一遍"拼 query + 翻页 + 游标护栏"，四处重复且护栏各不相同；
// 收敛到这里后，新接口只提供"取一页"的函数即可。

import (
	"fmt"
	"net/url"
	"strconv"
)

// maxPages 是自动翻页护栏：防服务端游标异常导致死循环（正常接口远小于此）。
const maxPages = 100

// pageQuery 拼分页 query：cursor 空则不传；limit<=0 不传，超过 max 截到 max（max<=0 不限制）。
func pageQuery(cursor string, limit, max int) url.Values {
	q := url.Values{}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		if max > 0 && limit > max {
			limit = max
		}
		q.Set("limit", strconv.Itoa(limit))
	}
	return q
}

// paginate 自动翻页：fetch 返回本页条目与下一页游标（空串=结束）。
// 护栏：最多 maxPages 页；游标必须前进（重复出现即报错，防服务端返回同一游标）。
func paginate[T any](fetch func(cursor string) (items []T, next string, err error)) ([]T, error) {
	var all []T
	cursor := ""
	for page := 0; page < maxPages; page++ {
		items, next, err := fetch(cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if next == "" {
			return all, nil
		}
		if next == cursor {
			return nil, fmt.Errorf("api: 分页游标不前进（%q）", cursor)
		}
		cursor = next
	}
	return nil, fmt.Errorf("api: 分页超过最大页数 %d", maxPages)
}
