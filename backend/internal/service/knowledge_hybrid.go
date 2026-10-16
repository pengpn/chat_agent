package service

import (
	"math"
	"sort"

	"github.com/example/agent-platform/backend/internal/repository"
)

const (
	RetrievalModeVector = "exact_vector"
	RetrievalModeHybrid = "hybrid_rrf"
)

// FuseKnowledgeCandidates 按片段 ID 合并两路排名；每一路最多贡献一次。
// RRF 分数只表达融合排序，不能作为余弦相似度或答案置信度解释。
func FuseKnowledgeCandidates(vector, keyword []repository.KnowledgeSearchRow, rankConstant int) []repository.KnowledgeSearchRow {
	if rankConstant <= 0 {
		rankConstant = 60
	}
	byID := make(map[string]repository.KnowledgeSearchRow, len(vector)+len(keyword))
	for index, row := range vector {
		if row.ChunkID == "" || math.IsNaN(row.Similarity) || math.IsInf(row.Similarity, 0) {
			continue
		}
		if existing, ok := byID[row.ChunkID]; ok && existing.VectorRank > 0 {
			continue
		}
		row.VectorRank, row.KeywordRank, row.KeywordScore = index+1, 0, 0
		row.FusionScore = 1 / float64(rankConstant+row.VectorRank)
		byID[row.ChunkID] = row
	}
	for index, row := range keyword {
		if row.ChunkID == "" || row.KeywordScore <= 0 || math.IsNaN(row.KeywordScore) || math.IsInf(row.KeywordScore, 0) || math.IsNaN(row.Similarity) || math.IsInf(row.Similarity, 0) {
			continue
		}
		existing, ok := byID[row.ChunkID]
		if ok && existing.KeywordRank > 0 {
			continue
		}
		if !ok {
			existing = row
			existing.VectorRank, existing.FusionScore = 0, 0
		}
		existing.KeywordRank, existing.KeywordScore = index+1, row.KeywordScore
		existing.FusionScore += 1 / float64(rankConstant+existing.KeywordRank)
		byID[row.ChunkID] = existing
	}
	result := make([]repository.KnowledgeSearchRow, 0, len(byID))
	for _, row := range byID {
		result = append(result, row)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.FusionScore != right.FusionScore {
			return left.FusionScore > right.FusionScore
		}
		if left.DocumentID != right.DocumentID {
			return left.DocumentID < right.DocumentID
		}
		if left.ChunkIndex != right.ChunkIndex {
			return left.ChunkIndex < right.ChunkIndex
		}
		return left.ChunkID < right.ChunkID
	})
	return result
}
