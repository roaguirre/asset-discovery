package runservice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
	current, err := s.loadRun(ctx, run.ID)
	if err != nil && status.Code(err) != codes.NotFound {
		return fmt.Errorf("load run %s: %w", run.ID, err)
	}
	if err == nil {
		run = preserveExecutionLease(current, run)
	}

	_, err = s.client.Collection("runs").Doc(run.ID).Set(ctx, run)
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

func (s *FirestoreProjectionStore) UpsertAsset(ctx context.Context, runID string, row AssetRow) error {
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

func (s *FirestoreProjectionStore) loadRun(ctx context.Context, runID string) (RunRecord, error) {
	snapshot, err := s.runDoc(runID).Get(ctx)
	if err != nil {
		return RunRecord{}, err
	}

	var run RunRecord
	if err := snapshot.DataTo(&run); err != nil {
		return RunRecord{}, fmt.Errorf("decode run %s: %w", runID, err)
	}
	return run, nil
}

func (s *FirestoreProjectionStore) ClaimRunExecution(
	ctx context.Context,
	runID string,
	leaseID string,
	now time.Time,
	ttl time.Duration,
) (RunRecord, bool, error) {
	ref := s.runDoc(runID)
	var claimed bool
	var run RunRecord

	err := s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(ref)
		if err != nil {
			return err
		}
		if err := snapshot.DataTo(&run); err != nil {
			return fmt.Errorf("decode run %s: %w", runID, err)
		}
		if leaseIsActive(run, now) && run.ExecutionLeaseID != leaseID {
			claimed = false
			return nil
		}

		heartbeatAt := now
		leaseUntil := now.Add(ttl)
		run.ExecutionLeaseID = leaseID
		run.ExecutionHeartbeatAt = &heartbeatAt
		run.ExecutionLeaseUntil = &leaseUntil
		claimed = true
		return tx.Set(ref, run)
	})
	if err != nil {
		return RunRecord{}, false, fmt.Errorf("claim run execution %s: %w", runID, err)
	}

	return run, claimed, nil
}

func (s *FirestoreProjectionStore) HeartbeatRunExecution(
	ctx context.Context,
	runID string,
	leaseID string,
	now time.Time,
	ttl time.Duration,
) error {
	ref := s.runDoc(runID)

	return s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(ref)
		if err != nil {
			return err
		}

		var run RunRecord
		if err := snapshot.DataTo(&run); err != nil {
			return fmt.Errorf("decode run %s: %w", runID, err)
		}
		if run.ExecutionLeaseID != leaseID {
			return fmt.Errorf("run %q lease mismatch", runID)
		}

		heartbeatAt := now
		leaseUntil := now.Add(ttl)
		run.ExecutionHeartbeatAt = &heartbeatAt
		run.ExecutionLeaseUntil = &leaseUntil
		return tx.Set(ref, run)
	})
}

func (s *FirestoreProjectionStore) ReleaseRunExecution(
	ctx context.Context,
	runID string,
	leaseID string,
	_ time.Time,
) error {
	ref := s.runDoc(runID)

	return s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(ref)
		if err != nil {
			return err
		}

		var run RunRecord
		if err := snapshot.DataTo(&run); err != nil {
			return fmt.Errorf("decode run %s: %w", runID, err)
		}
		if run.ExecutionLeaseID != "" && run.ExecutionLeaseID != leaseID {
			return fmt.Errorf("run %q lease mismatch", runID)
		}

		run.ExecutionLeaseID = ""
		run.ExecutionHeartbeatAt = nil
		run.ExecutionLeaseUntil = nil
		return tx.Set(ref, run)
	})
}

func firestoreJSONDocument(value interface{}) (map[string]interface{}, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	var payload interface{}
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}

	document, ok := normalizeFirestoreJSONValue(payload).(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("expected JSON object document, got %T", payload)
	}

	return document, nil
}

func normalizeFirestoreJSONValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		normalized := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			normalized[key] = normalizeFirestoreJSONValue(item)
		}
		return normalized
	case []interface{}:
		normalized := make([]interface{}, len(typed))
		for index, item := range typed {
			normalized[index] = normalizeFirestoreJSONValue(item)
		}
		return normalized
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if decimal, err := typed.Float64(); err == nil {
			return decimal
		}
		return typed.String()
	default:
		return value
	}
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
