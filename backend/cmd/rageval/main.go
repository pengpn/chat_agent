package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/example/agent-platform/backend/internal/config"
	"github.com/example/agent-platform/backend/internal/database"
	"github.com/example/agent-platform/backend/internal/embeddinggateway"
	"github.com/example/agent-platform/backend/internal/knowledgelex"
	"github.com/example/agent-platform/backend/internal/rageval"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/service"
	"github.com/google/uuid"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "rag evaluation:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("use 'capture', 'evaluate' or 'sweep'")
	}
	switch args[0] {
	case "capture":
		return capture(args[1:])
	case "evaluate":
		return evaluate(args[1:])
	case "sweep":
		return sweep(args[1:])
	default:
		return fmt.Errorf("unknown command %q; use 'capture', 'evaluate' or 'sweep'", args[0])
	}
}

// sweep 使用线上筛选逻辑重放同一批有序候选片段。
// 检索上下文变化后，旧回答及其忠实度标注不再有效，因此重放时会清除这些数据。
func sweep(args []string) error {
	flags := flag.NewFlagSet("sweep", flag.ContinueOnError)
	datasetPath := flags.String("dataset", "", "labeled dataset JSON")
	runPath := flags.String("run", "", "one candidate snapshot JSON")
	topKValues := flags.String("top-k", "", "comma-separated Top-K values")
	thresholdValues := flags.String("threshold", "", "comma-separated similarity thresholds")
	outPath := flags.String("out", "", "optional JSON report path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *datasetPath == "" || *runPath == "" || *topKValues == "" || *thresholdValues == "" {
		return errors.New("-dataset, -run, -top-k and -threshold are required")
	}
	dataset, err := readDataset(*datasetPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(*runPath)
	if err != nil {
		return err
	}
	var baseline rageval.Snapshot
	if err := json.Unmarshal(data, &baseline); err != nil {
		return err
	}
	if err := rageval.ValidateSnapshot(dataset, baseline); err != nil {
		return err
	}
	var snapshots []rageval.Snapshot
	for _, topKRaw := range strings.Split(*topKValues, ",") {
		topK, err := strconv.Atoi(strings.TrimSpace(topKRaw))
		if err != nil || topK <= 0 || topK > baseline.Parameters.CandidateLimit {
			return fmt.Errorf("invalid Top-K %q; capture enough candidates first", topKRaw)
		}
		for _, thresholdRaw := range strings.Split(*thresholdValues, ",") {
			threshold, err := strconv.ParseFloat(strings.TrimSpace(thresholdRaw), 64)
			if err != nil || threshold < 0 || threshold > 1 {
				return fmt.Errorf("invalid threshold %q", thresholdRaw)
			}
			variant := baseline
			variant.Name = fmt.Sprintf("%s-k%d-threshold%g", baseline.Name, topK, threshold)
			variant.Parameters.TopK = topK
			variant.Parameters.SimilarityThreshold = threshold
			variant.Cases = make([]rageval.Observation, len(baseline.Cases))
			for i, item := range baseline.Cases {
				item.Answer = ""
				item.AnswerCorrect = nil
				item.Claims = nil
				item.CitedDocumentIDs = nil
				item.GenerationMS = 0
				item.SelectedChunkIDs = nil
				variant.Cases[i] = item
			}
			snapshots = append(snapshots, variant)
		}
	}
	reports, err := rageval.Compare(dataset, snapshots)
	if err != nil {
		return err
	}
	data, err = json.MarshalIndent(reports, "", "  ")
	if err != nil {
		return err
	}
	return writeOutput(*outPath, data)
}

func evaluate(args []string) error {
	flags := flag.NewFlagSet("evaluate", flag.ContinueOnError)
	datasetPath := flags.String("dataset", "", "labeled dataset JSON")
	runPaths := flags.String("runs", "", "comma-separated captured or labeled snapshot JSON files")
	outPath := flags.String("out", "", "optional JSON report path; stdout by default")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *datasetPath == "" || *runPaths == "" {
		return errors.New("-dataset and -runs are required")
	}
	dataset, err := readDataset(*datasetPath)
	if err != nil {
		return err
	}
	paths := strings.Split(*runPaths, ",")
	snapshots := make([]rageval.Snapshot, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(strings.TrimSpace(path))
		if err != nil {
			return fmt.Errorf("read snapshot %s: %w", path, err)
		}
		var snapshot rageval.Snapshot
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return fmt.Errorf("decode snapshot %s: %w", path, err)
		}
		snapshots = append(snapshots, snapshot)
	}
	results, err := rageval.Compare(dataset, snapshots)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return writeOutput(*outPath, data)
}

func capture(args []string) error {
	flags := flag.NewFlagSet("capture", flag.ContinueOnError)
	datasetPath := flags.String("dataset", "", "dataset with real knowledge base IDs")
	userID := flags.String("user-id", "", "owner UUID (checked by repository on each query)")
	name := flags.String("name", "", "unique experiment name")
	outPath := flags.String("out", "", "snapshot output path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *datasetPath == "" || *name == "" || *outPath == "" {
		return errors.New("-dataset, -name and -out are required")
	}
	if err := uuid.Validate(*userID); err != nil {
		return fmt.Errorf("-user-id must be a UUID: %w", err)
	}
	dataset, err := readDataset(*datasetPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.Embedding.Enabled {
		return errors.New("EMBEDDING_ENABLED must be true for live capture")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	openCtx, cancelOpen := context.WithTimeout(ctx, 10*time.Second)
	db, sqlDB, err := database.Open(openCtx, database.Config{
		URL: cfg.DatabaseURL, MaxOpenConns: cfg.DatabaseMaxOpenConns, MaxIdleConns: cfg.DatabaseMaxIdleConns,
		ConnMaxLifetime: cfg.DatabaseConnMaxLifetime, ConnMaxIdleTime: cfg.DatabaseConnMaxIdleTime,
	})
	cancelOpen()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	store := repository.New(db).Knowledge
	embeddings, err := embeddinggateway.NewConfigured(cfg.Embedding, nil)
	if err != nil {
		return err
	}
	estimator, err := embeddinggateway.NewTokenEstimator(cfg.Embedding.Tokenizer)
	if err != nil {
		return err
	}
	retrieval := service.NewKnowledgeRetrieval(store, embeddings, estimator, service.KnowledgeRetrievalConfig{
		CandidateLimit: cfg.Knowledge.RetrievalCandidateLimit, TopK: cfg.Knowledge.RetrievalTopK,
		SimilarityThreshold: cfg.Knowledge.RetrievalSimilarityThreshold,
		ContextTokenBudget:  cfg.Knowledge.RetrievalContextTokenBudget,
		MaxKnowledgeBases:   cfg.Knowledge.RetrievalMaxKnowledgeBases, MaxQueryChars: cfg.Knowledge.RetrievalMaxQueryChars,
		Mode: cfg.Knowledge.RetrievalMode, RRFK: cfg.Knowledge.RetrievalRRFK,
	})
	baseIDs := make(map[string]bool)
	for _, item := range dataset.Cases {
		if len(item.KnowledgeBaseIDs) == 0 {
			return fmt.Errorf("case %q requires knowledgeBaseIds for live capture", item.ID)
		}
		for _, baseID := range item.KnowledgeBaseIDs {
			baseIDs[baseID] = true
		}
	}
	var sourceIDs []string
	var indexVersions []string
	knownDocuments := make(map[string]bool)
	for baseID := range baseIDs {
		if _, err := store.FindBaseByIDForUser(ctx, *userID, baseID); err != nil {
			return fmt.Errorf("knowledge base %s: %w", baseID, err)
		}
		documents, err := store.ListDocuments(ctx, *userID, baseID)
		if err != nil {
			return err
		}
		for _, document := range documents {
			sourceIDs = append(sourceIDs, document.ID+":"+document.Checksum)
			knownDocuments[document.ID] = true
			if document.Status == "ready" && document.IndexedAt != nil {
				indexVersions = append(indexVersions, fmt.Sprintf("%s:%d:%d:%s:%s:%d:%s", document.ID, document.IngestionVersion,
					document.ChunkCount, document.EmbeddingProvider, document.EmbeddingModel,
					document.EmbeddingDimension, document.IndexedAt.UTC().Format(time.RFC3339Nano)))
			}
		}
	}
	if len(sourceIDs) == 0 {
		return errors.New("no documents found in the selected knowledge bases")
	}
	for _, item := range dataset.Cases {
		for documentID := range item.RelevantDocuments {
			if !knownDocuments[documentID] {
				return fmt.Errorf("case %q labels unknown document %q", item.ID, documentID)
			}
		}
	}
	snapshot := rageval.Snapshot{
		SchemaVersion: rageval.SchemaVersion, DatasetID: dataset.ID, CorpusVersion: dataset.CorpusVersion,
		CorpusFingerprint: rageval.FingerprintSourceDocuments(sourceIDs),
		IndexFingerprint:  rageval.FingerprintSourceDocuments(indexVersions), Name: *name,
		Parameters: rageval.Parameters{
			ChunkTargetTokens: cfg.Knowledge.ChunkTargetTokens, ChunkOverlapTokens: cfg.Knowledge.ChunkOverlap,
			CandidateLimit: cfg.Knowledge.RetrievalCandidateLimit, TopK: cfg.Knowledge.RetrievalTopK,
			SimilarityThreshold: cfg.Knowledge.RetrievalSimilarityThreshold,
			ContextTokenBudget:  cfg.Knowledge.RetrievalContextTokenBudget,
			RetrievalMode:       cfg.Knowledge.RetrievalMode,
			RRFK:                cfg.Knowledge.RetrievalRRFK,
			KeywordVersion:      knowledgelex.Version,
			EmbeddingTokenizer:  cfg.Embedding.Tokenizer,
		},
		Cases: make([]rageval.Observation, 0, len(dataset.Cases)),
	}
	for _, item := range dataset.Cases {
		_, trace, err := retrieval.SearchWithTrace(ctx, *userID, service.KnowledgeRetrievalRequest{
			Query: item.Query, KnowledgeBaseIDs: item.KnowledgeBaseIDs,
		})
		if err != nil {
			return fmt.Errorf("case %q: %w", item.ID, err)
		}
		if snapshot.Parameters.EmbeddingModel == "" {
			snapshot.Parameters.EmbeddingProvider = trace.EmbeddingProvider
			snapshot.Parameters.EmbeddingModel = trace.EmbeddingModel
			snapshot.Parameters.EmbeddingDimension = trace.EmbeddingDimension
			snapshot.Parameters.PromptVersion = trace.PromptVersion
		} else if snapshot.Parameters.EmbeddingProvider != trace.EmbeddingProvider || snapshot.Parameters.EmbeddingModel != trace.EmbeddingModel || snapshot.Parameters.EmbeddingDimension != trace.EmbeddingDimension {
			return fmt.Errorf("case %q used a different embedding version", item.ID)
		}
		observed := rageval.Observation{CaseID: item.ID, Query: item.Query, EmbeddingMS: trace.EmbeddingMS, SearchMS: trace.SearchMS, KeywordSearchMS: trace.KeywordSearchMS, TotalMS: trace.TotalMS, SelectedChunkIDs: trace.SelectedChunkIDs}
		for _, candidate := range trace.Candidates {
			observed.Candidates = append(observed.Candidates, rageval.Candidate{
				KnowledgeBaseID: candidate.KnowledgeBaseID, DocumentID: candidate.DocumentID,
				DocumentName: candidate.DocumentName, ChunkID: candidate.ChunkID, ChunkIndex: candidate.ChunkIndex,
				Content: candidate.Content, ContentHash: candidate.ContentHash,
				TokenCount: candidate.TokenCount, Similarity: candidate.Similarity,
				VectorRank: candidate.VectorRank, KeywordRank: candidate.KeywordRank,
				KeywordScore: candidate.KeywordScore, FusionScore: candidate.FusionScore,
			})
		}
		snapshot.Cases = append(snapshot.Cases, observed)
	}
	if err := rageval.ValidateSnapshot(dataset, snapshot); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return writeOutput(*outPath, data)
}

func readDataset(path string) (rageval.Dataset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return rageval.Dataset{}, err
	}
	return rageval.DecodeDataset(data)
}

func writeOutput(path string, data []byte) error {
	data = append(data, '\n')
	if path == "" {
		_, err := os.Stdout.Write(data)
		return err
	}
	// 使用独占创建，避免后续抓取覆盖已经完成标注的评估快照。
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(data)
	return err
}
