package service

import (
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/rerankgateway"
)

func (s *KnowledgeRetrievalService) ConfigureReranker(gateway rerankgateway.Gateway) {
	s.reranker = gateway
}

// ApplyKnowledgeRerank 按模型返回的输入索引映射原始片段，保留全部来源和原有分数。
// 同一函数也用于离线重放，供应商返回的正文不会替换数据库中的可信快照。
func ApplyKnowledgeRerank(candidates []repository.KnowledgeSearchRow, items []rerankgateway.Item) ([]repository.KnowledgeSearchRow, error) {
	ordered, err := rerankgateway.Order(items, len(candidates))
	if err != nil {
		return nil, err
	}
	result := make([]repository.KnowledgeSearchRow, 0, len(candidates))
	for rank, item := range ordered {
		row := candidates[item.Index]
		row.PreRerankRank, row.RerankRank, row.RerankScore = item.Index+1, rank+1, item.Score
		result = append(result, row)
	}
	return result, nil
}

func knowledgeRerankScore(row repository.KnowledgeSearchRow) *float64 {
	if row.RerankRank <= 0 {
		return nil
	}
	score := row.RerankScore
	return &score
}
