package runservice

import (
	"context"
	"encoding/json"
	"fmt"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"

	"asset-discovery/internal/export/visualizer"
	"asset-discovery/internal/tracing/lineage"
)

type FirestoreProjectionStore struct {
	client *firestore.Client
}

const firestoreBatchLimit = 500

func NewFirestoreProjectionStore(client *firestore.Client) *FirestoreProjectionStore {
	return &FirestoreProjectionStore{client: client}
}

func (s *FirestoreProjectionStore) UpsertRun(ctx context.Context, run RunRecord) error {
	_, err := s.client.Collection("runs").Doc(run.ID).Set(ctx, run)
	return err
}

func (s *FirestoreProjectionStore) UpsertSeed(ctx context.Context, runID string, seed SeedRecord) error {
	_, err := s.runDoc(runID).Collection("seeds").Doc(seed.ID).Set(ctx, seed)
	return err
}

func (s *FirestoreProjectionStore) UpsertPivot(ctx context.Context, runID string, pivot PivotRecord) error {
	_, err := s.runDoc(runID).Collection("pivots").Doc(pivot.ID).Set(ctx, pivot)
	return err
}

func (s *FirestoreProjectionStore) UpsertJudgeSummary(
	ctx context.Context,
	runID string,
	summary lineage.JudgeSummary,
) error {
	payload, err := firestoreJSONDocument(summary)
	if err != nil {
		return fmt.Errorf("marshal judge summary: %w", err)
	}
	_, err = s.runDoc(runID).Collection("analysis").Doc("judge_summary").Set(ctx, payload)
	return err
}

func (s *FirestoreProjectionStore) AppendEvent(ctx context.Context, runID string, event EventRecord) error {
	_, err := s.runDoc(runID).Collection("events").Doc(event.ID).Set(ctx, event)
	return err
}

func (s *FirestoreProjectionStore) UpsertAsset(ctx context.Context, runID string, row visualizer.Row) error {
	payload, err := firestoreJSONDocument(row)
	if err != nil {
		return fmt.Errorf("marshal asset row: %w", err)
	}
	_, err = s.runDoc(runID).Collection("assets").Doc(row.AssetID).Set(ctx, payload)
	return err
}

func (s *FirestoreProjectionStore) SyncTraces(ctx context.Context, runID string, traces []lineage.Trace) error {
	traceCollection := s.runDoc(runID).Collection("traces")
	want := make(map[string]map[string]interface{}, len(traces))
	for _, trace := range traces {
		payload, err := firestoreJSONDocument(trace)
		if err != nil {
			return fmt.Errorf("marshal trace %s: %w", trace.AssetID, err)
		}
		want[trace.AssetID] = payload
	}

	mutations := make([]firestoreMutation, 0, len(want))
	for assetID, payload := range want {
		mutations = append(mutations, firestoreMutation{
			ref:     traceCollection.Doc(assetID),
			payload: payload,
		})
	}

	iter := traceCollection.Documents(ctx)
	defer iter.Stop()
	for {
		doc, err := iter.Next()
		if err != nil {
			if err == iterator.Done {
				break
			}
			return fmt.Errorf("iterate traces: %w", err)
		}
		if _, ok := want[doc.Ref.ID]; ok {
			continue
		}
		mutations = append(mutations, firestoreMutation{
			ref:    doc.Ref,
			delete: true,
		})
	}

	return s.commitMutations(ctx, mutations)
}

func (s *FirestoreProjectionStore) runDoc(runID string) *firestore.DocumentRef {
	return s.client.Collection("runs").Doc(runID)
}

func firestoreJSONDocument(value interface{}) (map[string]interface{}, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

type firestoreMutation struct {
	ref     *firestore.DocumentRef
	payload map[string]interface{}
	delete  bool
}

func (s *FirestoreProjectionStore) commitMutations(ctx context.Context, mutations []firestoreMutation) error {
	if len(mutations) == 0 {
		return nil
	}

	for start := 0; start < len(mutations); start += firestoreBatchLimit {
		end := start + firestoreBatchLimit
		if end > len(mutations) {
			end = len(mutations)
		}

		batch := s.client.Batch()
		for _, mutation := range mutations[start:end] {
			if mutation.delete {
				batch.Delete(mutation.ref)
				continue
			}
			batch.Set(mutation.ref, mutation.payload)
		}

		if _, err := batch.Commit(ctx); err != nil {
			return fmt.Errorf("commit firestore batch: %w", err)
		}
	}

	return nil
}
