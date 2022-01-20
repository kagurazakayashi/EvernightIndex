package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// 本檔實作根目錄綁定與查詢。
//
// 一庫一根目錄由結構層的 CHECK (id = 1) 保證；綁定後不自動改綁，
// 路徑或卷身份不一致時回報 E_ROOT_MISMATCH，讓使用者明確決定怎麼處理。

// Root 讀取已綁定的根目錄；尚未綁定時 found 為 false。
func (s *Store) Root(ctx context.Context) (model.Root, bool, error) {
	var root model.Root
	var casePolicy string
	var volume, fileID, publishedRun, publishedAt sql.NullString
	var publishedGeneration sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, abs_path, path_key, source_platform, case_policy,
		       volume_identity, root_file_id, bound_at_utc,
		       published_generation, published_run_id, published_at_utc
		FROM root_dir WHERE id = 1`).Scan(
		&root.ID, &root.AbsPath, &root.PathKey, &root.SourcePlatform, &casePolicy,
		&volume, &fileID, &root.BoundAt,
		&publishedGeneration, &publishedRun, &publishedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Root{}, false, nil
	}
	if err != nil {
		return model.Root{}, false, databaseError(err)
	}
	root.CasePolicy = model.CasePolicy(casePolicy)
	root.VolumeIdentity = stringPointer(volume)
	root.RootFileID = stringPointer(fileID)
	if publishedGeneration.Valid {
		generation := publishedGeneration.Int64
		root.PublishedGeneration = &generation
	}
	root.PublishedRunID = stringPointer(publishedRun)
	if publishedAt.Valid {
		stamp := model.Timestamp(publishedAt.String)
		root.PublishedAt = &stamp
	}
	return root, true, nil
}

// BindRoot 綁定或驗證根目錄。
//
// 首次呼叫建立綁定；之後每次開啟都比對路徑鍵與已知的卷身份：
//   - 路徑不同 → E_ROOT_MISMATCH，不自動改綁；
//   - 卷身份兩邊都有值但不同 → 視為根目錄被替換，同樣拒絕；
//   - 只有大小寫或分隔符差異（同一策略下等價）→ 接受，並保留原始綁定值。
func (s *Store) BindRoot(ctx context.Context, spec model.RootSpec) (model.Root, error) {
	absPath := strings.TrimSpace(spec.AbsPath)
	if absPath == "" {
		return model.Root{}, model.New(model.CodeInternal, "err.internal", model.ExitFatal, map[string]string{
			"detail": "root path is empty",
		})
	}
	if !spec.CasePolicy.Valid() {
		return model.Root{}, model.New(model.CodeInternal, "err.internal", model.ExitFatal, map[string]string{
			"detail": "unknown case policy " + string(spec.CasePolicy),
		})
	}
	if spec.SourcePlatform == "" {
		return model.Root{}, model.New(model.CodeInternal, "err.internal", model.ExitFatal, map[string]string{
			"detail": "source platform is empty",
		})
	}

	existing, found, err := s.Root(ctx)
	if err != nil {
		return model.Root{}, err
	}
	if found {
		// 既有綁定的策略優先：跨系統讀取時不要用目前系統的假設覆蓋來源環境。
		if model.PathKey(absPath, existing.CasePolicy) != existing.PathKey {
			return model.Root{}, model.New(model.CodeRootMismatch, "err.rootMismatch", model.ExitFatal, map[string]string{
				"expected": existing.AbsPath,
				"found":    absPath,
			})
		}
		if existing.VolumeIdentity != nil && spec.VolumeIdentity != nil && *existing.VolumeIdentity != *spec.VolumeIdentity {
			return model.Root{}, model.New(model.CodeRootMismatch, "err.rootMismatch", model.ExitFatal, map[string]string{
				"expected": existing.AbsPath + " (volume " + *existing.VolumeIdentity + ")",
				"found":    absPath + " (volume " + *spec.VolumeIdentity + ")",
			})
		}
		return existing, nil
	}

	pathKey := model.PathKey(absPath, spec.CasePolicy)
	err = s.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, `
			INSERT INTO root_dir(id, abs_path, path_key, source_platform, case_policy,
			                     volume_identity, root_file_id, bound_at_utc)
			VALUES (1, ?, ?, ?, ?, ?, ?, ?)`,
			absPath, pathKey, spec.SourcePlatform, string(spec.CasePolicy),
			nullableString(spec.VolumeIdentity), nullableString(spec.RootFileID),
			model.NowTimestamp().String(),
		)
		return execErr
	})
	if err != nil {
		return model.Root{}, err
	}
	root, _, err := s.Root(ctx)
	return root, err
}

// PublishedGeneration 回傳已發佈代次；尚未發佈或尚未綁定時回傳 0。
//
// 查詢端必須把 0 當成「沒有完整索引」：首次建立被取消或發佈失敗時，
// 資料庫就是這個狀態。
func (s *Store) PublishedGeneration(ctx context.Context) (int64, error) {
	root, found, err := s.Root(ctx)
	if err != nil {
		return 0, err
	}
	if !found || root.PublishedGeneration == nil {
		return 0, nil
	}
	return *root.PublishedGeneration, nil
}

// stringPointer 把可為 NULL 的字串轉成指標。
func stringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}

// nullableString 把指標轉成 SQL 參數：nil 寫入 NULL。
func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// nullableGeneration 把代次轉成 SQL 參數：0 代表「尚無已發佈代次」，寫入 NULL。
func nullableGeneration(generation int64) any {
	if generation <= 0 {
		return nil
	}
	return generation
}
