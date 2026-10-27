package database

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"projectmap/internal/memory"
	"strings"
	"testing"
	"time"
)

func TestMemoryRecordPreservesRevisionsAndNormalizesTime(t *testing.T) {
	row, err := decodeMemory([]byte(`{"id":"11111111-1111-4111-8111-111111111111","profile_id":"owner","revision":9007199254740993,"created_at":"2026-10-26T18:00:00+05:30","updated_at":"2026-10-26T18:00:00+05:30","archived_at":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if memory.Int(row["revision"]) != 9007199254740993 {
		t.Fatal("revision lost integer precision")
	}
	if row["created_at"].(time.Time).Location() != time.UTC {
		t.Fatal("timestamp not normalized")
	}
	store := &MemoryStore{origin: "http://127.0.0.1:8080"}
	dto := store.projectDTO(row)
	if _, found := dto["profile_id"]; found {
		t.Fatal("internal owner field exposed")
	}
	if row["profile_id"] != "owner" || dto["dashboard_url"] != "http://127.0.0.1:8080/projects/11111111-1111-4111-8111-111111111111" {
		t.Fatal("projection mutated storage or returned wrong link")
	}
}
func TestMemoryStorageErrorsAreSanitized(t *testing.T) {
	for _, err := range []error{errors.New("secret database credentials"), &pgconn.PgError{Code: "08006", Message: "secret database credentials"}, &pgconn.PgError{Code: "23514", Message: "secret SQL value"}, context.DeadlineExceeded} {
		result := memoryError(err)
		var problem *memory.Error
		if !errors.As(result, &problem) || strings.Contains(result.Error(), "secret") {
			t.Fatal("storage error leaked")
		}
	}
	original := memory.Problem(404, "not_found", "Not found.")
	if memoryError(original) != original {
		t.Fatal("domain error was lost")
	}
}
