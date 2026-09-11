package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/example/agent-platform/backend/internal/database/model"
)

type chunkTokenEstimator interface {
	Estimate(text string) int64
}

type knowledgeChunkConfig struct {
	TargetTokens  int64
	OverlapTokens int64
	MinTokens     int64
}

type chunkPart struct {
	text         string
	pageNumber   *int
	sectionTitle string
	startChar    int
	endChar      int
}

func buildKnowledgeChunks(document extractedDocument, estimator chunkTokenEstimator, cfg knowledgeChunkConfig) []model.KnowledgeChunk {
	coreLimit := cfg.TargetTokens - cfg.OverlapTokens
	if coreLimit <= 0 {
		coreLimit = cfg.TargetTokens
	}
	parts := make([]chunkPart, 0, len(document.Blocks))
	for _, block := range document.Blocks {
		parts = append(parts, splitChunkPart(chunkPart{
			text: block.Text, pageNumber: block.PageNumber, sectionTitle: block.SectionTitle,
			startChar: block.StartChar, endChar: block.EndChar,
		}, estimator, coreLimit)...)
	}

	var cores []chunkPart
	var current chunkPart
	for _, part := range parts {
		if strings.TrimSpace(part.text) == "" {
			continue
		}
		candidate := part.text
		if current.text != "" {
			candidate = current.text + "\n\n" + part.text
		}
		if current.text != "" && estimator.Estimate(candidate) > coreLimit {
			cores = append(cores, current)
			current = chunkPart{}
		}
		if current.text == "" {
			current = part
			continue
		}
		current.text += "\n\n" + part.text
		current.endChar = part.endChar
		if current.sectionTitle == "" {
			current.sectionTitle = part.sectionTitle
		}
		if !samePage(current.pageNumber, part.pageNumber) {
			current.pageNumber = nil
		}
	}
	if current.text != "" {
		cores = append(cores, current)
	}
	if len(cores) > 1 && estimator.Estimate(cores[len(cores)-1].text) < cfg.MinTokens {
		last := cores[len(cores)-1]
		previous := &cores[len(cores)-2]
		previous.text += "\n\n" + last.text
		previous.endChar = last.endChar
		if !samePage(previous.pageNumber, last.pageNumber) {
			previous.pageNumber = nil
		}
		cores = cores[:len(cores)-1]
	}

	chunks := make([]model.KnowledgeChunk, 0, len(cores))
	previousContent := ""
	previousEnd := 0
	for index, core := range cores {
		content := core.text
		overlap := ""
		startChar := core.startChar
		if index > 0 && cfg.OverlapTokens > 0 {
			overlap = tokenBoundedSuffix(previousContent, estimator, cfg.OverlapTokens)
			if overlap != "" {
				content = overlap + "\n\n" + content
				startChar = previousEnd - len([]rune(overlap))
				if startChar < 0 {
					startChar = 0
				}
			}
		}
		metadata, _ := json.Marshal(map[string]any{
			"startChar": startChar, "endChar": core.endChar,
			"primaryStartChar": core.startChar, "primaryEndChar": core.endChar,
			"overlapTokens": estimator.Estimate(overlap),
		})
		hash := sha256.Sum256([]byte(content))
		chunks = append(chunks, model.KnowledgeChunk{
			ChunkIndex: index, Content: content, ContentHash: hex.EncodeToString(hash[:]),
			TokenCount: estimator.Estimate(content), PageNumber: cloneInt(core.pageNumber),
			SectionTitle: core.sectionTitle, Metadata: model.JSONDocument(metadata),
		})
		previousContent = content
		previousEnd = core.endChar
	}
	return chunks
}

func splitChunkPart(part chunkPart, estimator chunkTokenEstimator, limit int64) []chunkPart {
	if estimator.Estimate(part.text) <= limit {
		return []chunkPart{part}
	}
	runes := []rune(part.text)
	parts := make([]chunkPart, 0, 2)
	for offset := 0; offset < len(runes); {
		low, high, best := offset+1, len(runes), offset+1
		for low <= high {
			mid := low + (high-low)/2
			if estimator.Estimate(string(runes[offset:mid])) <= limit {
				best = mid
				low = mid + 1
			} else {
				high = mid - 1
			}
		}
		text := strings.TrimSpace(string(runes[offset:best]))
		if text != "" {
			parts = append(parts, chunkPart{
				text: text, pageNumber: cloneInt(part.pageNumber), sectionTitle: part.sectionTitle,
				startChar: part.startChar + offset, endChar: part.startChar + best,
			})
		}
		offset = best
	}
	return parts
}

func tokenBoundedSuffix(text string, estimator chunkTokenEstimator, limit int64) string {
	runes := []rune(text)
	low, high, best := 0, len(runes), len(runes)
	for low <= high {
		mid := low + (high-low)/2
		candidate := strings.TrimSpace(string(runes[mid:]))
		if estimator.Estimate(candidate) <= limit {
			best = mid
			high = mid - 1
		} else {
			low = mid + 1
		}
	}
	return strings.TrimSpace(string(runes[best:]))
}

func samePage(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
