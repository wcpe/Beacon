package service

import "context"

// applyPreviewForTest 仅供既有算法测试覆盖已批准后的私有读取路径。
func (s *AssetPreviewService) applyPreviewForTest(ctx context.Context, params PreviewParams) (*AssetPreviewResult, error) {
	return s.applyPreview(ctx, params)
}

// applyDiffForTest 仅供既有算法测试覆盖已批准后的私有差异路径。
func (s *AssetPreviewService) applyDiffForTest(ctx context.Context, params DiffParams) (*AssetDiffResult, error) {
	return s.applyDiff(ctx, params)
}
