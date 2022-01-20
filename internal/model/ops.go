package model

// OpKind 是一次逐條操作記錄的種類，供 -v 輸出與統計使用。
//
// 這些值同時是穩定的機器標識，不隨語言改變；人類可讀文字由 i18n 資源提供。
type OpKind string

const (
	// OpAdded 表示新增條目。
	OpAdded OpKind = "added"
	// OpUpdated 表示更新條目。
	OpUpdated OpKind = "updated"
	// OpRemoved 表示保守確認移除條目。
	OpRemoved OpKind = "removed"
	// OpExcluded 表示因排除規則移出索引。
	OpExcluded OpKind = "excluded"
	// OpHashed 表示重新計算摘要。
	OpHashed OpKind = "hashed"
	// OpFailed 表示處理失敗。
	OpFailed OpKind = "failed"
)

// MessageKey 回傳此操作種類對應的 i18n 訊息鍵。
func (k OpKind) MessageKey() string {
	switch k {
	case OpAdded:
		return "op.added"
	case OpUpdated:
		return "op.updated"
	case OpRemoved:
		return "op.removed"
	case OpExcluded:
		return "op.excluded"
	case OpHashed:
		return "op.hashed"
	case OpFailed:
		return "op.failed"
	default:
		return "err.internal"
	}
}
