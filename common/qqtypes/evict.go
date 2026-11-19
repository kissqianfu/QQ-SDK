package qqtypes

// EvictFIFO 按先进先出从 m 删掉一个键，返回裁剪后的顺序表（跳过已不存在的键）。
// 供各处「键表上限淘汰」共用：调用方在键数达到上限时调用一次即可。
func EvictFIFO[V any](m map[string]V, order []string) []string {
	for len(order) > 0 {
		k := order[0]
		order = order[1:]
		if _, ok := m[k]; ok {
			delete(m, k)
			return order
		}
	}
	return order
}
