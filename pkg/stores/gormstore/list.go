package gormstore

// list 保证列表查询返回非 nil 切片：GORM Find 空结果时目标切片
// 保持 nil，JSON 序列化为 null——Console 前端按数组消费时需逐处
// 判空。存储层统一兜底为空切片（[] 语义），消费方零防御。
func list[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
