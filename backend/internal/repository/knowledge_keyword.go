package repository

import (
	"context"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/knowledgelex"
	pgvector "github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
)

func prepareKnowledgeKeywords(chunks []model.KnowledgeChunk) {
	for i := range chunks {
		chunks[i].KeywordText = knowledgelex.IndexText(chunks[i].Content)
		chunks[i].KeywordVersion = knowledgelex.Version
	}
}

// migrateKnowledgeKeywordIndex 对旧片段分批回填关键词，失败后下次启动可继续。
// 版本字段让只含符号的空分词结果也能被标记为已处理，避免重复回填。
func migrateKnowledgeKeywordIndex(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "knowledge_keywords_v1").Error; err != nil {
			return err
		}
		var batch []model.KnowledgeChunk
		result := tx.Select("id", "content").Where("keyword_version < ?", knowledgelex.Version).
			FindInBatches(&batch, 200, func(_ *gorm.DB, _ int) error {
				for _, chunk := range batch {
					if err := tx.Model(&model.KnowledgeChunk{}).Where("id = ? AND keyword_version < ?", chunk.ID, knowledgelex.Version).
						UpdateColumns(map[string]any{"keyword_text": knowledgelex.IndexText(chunk.Content), "keyword_version": knowledgelex.Version}).Error; err != nil {
						return err
					}
				}
				return nil
			})
		if result.Error != nil {
			return result.Error
		}
		return tx.Exec(`CREATE INDEX IF NOT EXISTS knowledge_chunks_keyword_gin_idx
			ON knowledge_chunks USING GIN (to_tsvector('simple', keyword_text))`).Error
	})
}

// SearchChunksKeyword 独立召回关键词命中的片段，使用与向量查询相同的权限、
// 就绪状态和向量版本约束。余弦相似度只作为诊断字段，不作为关键词命中门槛。
func (r *GormKnowledgeRepository) SearchChunksKeyword(ctx context.Context, userID string, baseIDs []string, providerName, embeddingModel string, dimension, limit int, queryText string, query pgvector.Vector) ([]KnowledgeSearchRow, error) {
	tsQuery := knowledgelex.TSQuery(queryText)
	if len(baseIDs) == 0 || limit <= 0 || tsQuery == "" {
		return []KnowledgeSearchRow{}, nil
	}
	items := make([]KnowledgeSearchRow, 0, limit)
	err := r.db.WithContext(ctx).Raw(`
		WITH search_query AS (SELECT to_tsquery('simple', ?) AS keywords, ?::vector AS embedding)
		SELECT d.knowledge_base_id, c.document_id, d.original_name AS document_name,
			c.id AS chunk_id, c.chunk_index, c.content, c.content_hash, c.token_count,
			c.page_number, c.section_title, c.metadata,
			1 - (c.embedding <=> q.embedding) AS similarity,
			ts_rank(to_tsvector('simple', c.keyword_text), q.keywords, 32) AS keyword_score
		FROM knowledge_chunks c
		JOIN knowledge_documents d ON d.id = c.document_id
		JOIN knowledge_bases b ON b.id = d.knowledge_base_id
		CROSS JOIN search_query q
		WHERE b.user_id = ? AND d.knowledge_base_id IN ? AND d.status = ?
			AND d.embedding_provider = ? AND d.embedding_model = ? AND d.embedding_dimension = ?
			AND c.embedding IS NOT NULL AND c.embedding_model = ? AND c.embedding_dimension = ?
			AND c.keyword_version = ? AND to_tsvector('simple', c.keyword_text) @@ q.keywords
		ORDER BY keyword_score DESC, c.document_id ASC, c.chunk_index ASC, c.id ASC
		LIMIT ?
	`, tsQuery, query, userID, baseIDs, model.KnowledgeDocumentStatusReady,
		providerName, embeddingModel, dimension, embeddingModel, dimension, knowledgelex.Version, limit).Scan(&items).Error
	return items, err
}
