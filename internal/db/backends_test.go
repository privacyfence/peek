package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/privacyfence/peek/internal/models"
	"github.com/privacyfence/peek/internal/uploadquota"
)

func TestUploadBackendRoundTripAndUpsert(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	ctx := context.Background()
	owner, id := seedVersionUpload(t, s, "o@example.test", "be-a", 1)
	if _, err := s.GetUploadBackend(ctx, id, 1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows, got %v", err)
	}
	if err := s.SetUploadBackend(ctx, models.UploadBackend{UploadID: id, Version: 1, Image: "ghcr.io/a/b:1", Port: 8080, CreatedByAccountID: owner}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetUploadBackend(ctx, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Image != "ghcr.io/a/b:1" || got.Port != 8080 || got.CreatedByAccountID != owner || got.CreatedAt.IsZero() {
		t.Fatalf("unexpected backend: %+v", got)
	}
	if err := s.SetUploadBackend(ctx, models.UploadBackend{UploadID: id, Version: 1, Image: "ghcr.io/a/b:2", Port: 9000}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetUploadBackend(ctx, id, 1)
	if got.Image != "ghcr.io/a/b:2" || got.Port != 9000 {
		t.Fatalf("upsert did not replace: %+v", got)
	}
	if got.CreatedByAccountID != owner {
		t.Fatalf("upsert should keep creator, got %d", got.CreatedByAccountID)
	}
}

func TestUploadBackendZeroCreatorIsNull(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	ctx := context.Background()
	_, id := seedVersionUpload(t, s, "o@example.test", "be-null", 1)
	if err := s.SetUploadBackend(ctx, models.UploadBackend{UploadID: id, Version: 1, Image: "x", Port: 80}); err != nil {
		t.Fatal(err)
	}
	var creator sql.NullInt64
	if err := s.QueryRow(`SELECT created_by_account_id FROM upload_backends WHERE upload_id=?`, id).Scan(&creator); err != nil || creator.Valid {
		t.Fatalf("creator = %+v (err %v), want NULL", creator, err)
	}
}

func TestListAndDeleteUploadBackend(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	ctx := context.Background()
	owner, id := seedVersionUpload(t, s, "o@example.test", "be-b", 1)
	if _, err := s.CreateVersionChecked(ctx, id, "k2", 1, owner, uploadquota.Limits{}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []int{2, 1} {
		if err := s.SetUploadBackend(ctx, models.UploadBackend{UploadID: id, Version: v, Image: "img", Port: 8080}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListUploadBackends(ctx, id)
	if err != nil || len(list) != 2 || list[0].Version != 1 || list[1].Version != 2 {
		t.Fatalf("list = %+v (err %v)", list, err)
	}
	if err := s.DeleteUploadBackend(ctx, id, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUploadBackend(ctx, id, 1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows, got %v", err)
	}
	// Removing the second version removes only its backend row.
	if err := s.DeleteUploadVersion(ctx, id, 2); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListUploadBackends(ctx, id); len(list) != 0 {
		t.Fatalf("expected no backends, got %+v", list)
	}
}

func TestDeleteUploadVersionKeepsOtherBackends(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	ctx := context.Background()
	owner, id := seedVersionUpload(t, s, "o@example.test", "be-c", 1)
	if _, err := s.CreateVersionChecked(ctx, id, "k2", 1, owner, uploadquota.Limits{}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []int{1, 2} {
		if err := s.SetUploadBackend(ctx, models.UploadBackend{UploadID: id, Version: v, Image: "img", Port: 8080}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteUploadVersion(ctx, id, 1); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListUploadBackends(ctx, id)
	if len(list) != 1 || list[0].Version != 2 {
		t.Fatalf("unexpected backends: %+v", list)
	}
}

func TestSetUploadBackendMissingVersionFails(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	_, id := seedVersionUpload(t, s, "o@example.test", "be-d", 1)
	if err := s.SetUploadBackend(context.Background(), models.UploadBackend{UploadID: id, Version: 9, Image: "img", Port: 80}); err == nil {
		t.Fatal("expected a foreign key error")
	}
}

func TestUploadBackendEnv(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	ctx := context.Background()
	_, id := seedVersionUpload(t, s, "o@example.test", "be-e", 1)
	if err := s.DeleteUploadBackendEnv(ctx, id, "NOPE"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows, got %v", err)
	}
	for _, kv := range [][2]string{{"ZED", "1"}, {"ALPHA", "2"}, {"MID", "3"}} {
		if err := s.SetUploadBackendEnv(ctx, id, kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetUploadBackendEnv(ctx, id, "MID", "new"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListUploadBackendEnv(ctx, id)
	if err != nil || len(list) != 3 {
		t.Fatalf("list = %+v (err %v)", list, err)
	}
	if list[0].Name != "ALPHA" || list[1].Name != "MID" || list[2].Name != "ZED" || list[1].Value != "new" || list[0].UploadID != id || list[0].UpdatedAt.IsZero() {
		t.Fatalf("unexpected order or content: %+v", list)
	}
	if err := s.DeleteUploadBackendEnv(ctx, id, "MID"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListUploadBackendEnv(ctx, id); len(list) != 2 {
		t.Fatalf("expected 2 vars, got %+v", list)
	}
}

func TestDeleteUploadRemovesBackendRows(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	ctx := context.Background()
	_, id := seedVersionUpload(t, s, "o@example.test", "be-f", 1)
	if err := s.SetUploadBackend(ctx, models.UploadBackend{UploadID: id, Version: 1, Image: "img", Port: 80}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUploadBackendEnv(ctx, id, "A", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUpload(ctx, id); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListUploadBackends(ctx, id); len(list) != 0 {
		t.Fatalf("backends remain: %+v", list)
	}
	if list, _ := s.ListUploadBackendEnv(ctx, id); len(list) != 0 {
		t.Fatalf("env remains: %+v", list)
	}
}

func TestUploadBackendTokenCallsDefaultsOff(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	_, id := seedVersionUpload(t, s, "o@example.test", "tc-a", 1)
	on, err := s.UploadBackendTokenCalls(context.Background(), id)
	if err != nil || on {
		t.Fatalf("on=%v err=%v, want false, nil", on, err)
	}
}

func TestSetUploadBackendTokenCallsUpsert(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	ctx := context.Background()
	_, id := seedVersionUpload(t, s, "o@example.test", "tc-b", 1)
	for _, want := range []bool{true, false, true} {
		if err := s.SetUploadBackendTokenCalls(ctx, id, want); err != nil {
			t.Fatal(err)
		}
		got, err := s.UploadBackendTokenCalls(ctx, id)
		if err != nil || got != want {
			t.Fatalf("got %v err %v, want %v", got, err, want)
		}
	}
}

func TestRecordBackendTokenCallCounts(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	ctx := context.Background()
	a, id := seedVersionUpload(t, s, "o@example.test", "tc-c", 1)
	b, err := s.CreateAccount(ctx, "", "robot", false)
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.RecordBackendTokenCall(ctx, id, 1, a, 200, false))
	must(s.RecordBackendTokenCall(ctx, id, 1, a, 500, true))
	must(s.RecordBackendTokenCall(ctx, id, 1, a, 500, false))
	must(s.RecordBackendTokenCall(ctx, id, 2, b.ID, 200, false))
	_, err = s.Exec(`UPDATE backend_token_calls SET last_call_at=1000 WHERE account_id=?`, a)
	must(err)
	_, err = s.Exec(`UPDATE backend_token_calls SET last_call_at=2000 WHERE account_id=?`, b.ID)
	must(err)
	list, err := s.ListBackendTokenCalls(ctx, id, 100)
	must(err)
	if len(list) != 2 {
		t.Fatalf("rows = %+v", list)
	}
	if list[0].AccountID != b.ID || list[0].Version != 2 || list[0].Email != "" || list[0].Calls != 1 || list[0].Identified {
		t.Fatalf("first row = %+v", list[0])
	}
	r := list[1]
	if r.AccountID != a || r.Calls != 3 || r.LastStatus != 500 || !r.Identified || r.Email != "o@example.test" || r.LastCallAt.Unix() != 1000 {
		t.Fatalf("second row = %+v", r)
	}
}

func TestListBackendTokenCallsLimit(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	ctx := context.Background()
	a, id := seedVersionUpload(t, s, "o@example.test", "tc-d", 1)
	for v := 1; v <= 2; v++ {
		if err := s.RecordBackendTokenCall(ctx, id, v, a, 200, false); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListBackendTokenCalls(ctx, id, 1)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v err %v", list, err)
	}
}

func TestBackendTokenCallsCascadeOnUploadDelete(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	ctx := context.Background()
	a, id := seedVersionUpload(t, s, "o@example.test", "tc-e", 1)
	if err := s.SetUploadBackendTokenCalls(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordBackendTokenCall(ctx, id, 1, a, 200, false); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUpload(ctx, id); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"upload_backend_settings", "backend_token_calls"} {
		var n int
		if err := s.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE upload_id=?`, id).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s rows = %d err %v", table, n, err)
		}
	}
}

func TestBackendTokenCallStoreErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("get", func(t *testing.T) {
		s := openTestStore(t)
		defer s.Close()
		_, id := seedVersionUpload(t, s, "o@example.test", "tc-f", 1)
		if _, err := s.Exec(`DROP TABLE upload_backend_settings`); err != nil {
			t.Fatal(err)
		}
		_, err := s.UploadBackendTokenCalls(ctx, id)
		if err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("set", func(t *testing.T) {
		s := openTestStore(t)
		defer s.Close()
		_, id := seedVersionUpload(t, s, "o@example.test", "tc-g", 1)
		failOn(t, s, "upload_backend_settings", "INSERT")
		wantErr(t, "set", s.SetUploadBackendTokenCalls(ctx, id, true), errInjected)
	})
	t.Run("record", func(t *testing.T) {
		s := openTestStore(t)
		defer s.Close()
		a, id := seedVersionUpload(t, s, "o@example.test", "tc-h", 1)
		failOn(t, s, "backend_token_calls", "INSERT")
		wantErr(t, "record", s.RecordBackendTokenCall(ctx, id, 1, a, 200, false), errInjected)
	})
	t.Run("list query", func(t *testing.T) {
		s := openTestStore(t)
		defer s.Close()
		_, id := seedVersionUpload(t, s, "o@example.test", "tc-i", 1)
		if _, err := s.Exec(`DROP TABLE backend_token_calls`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ListBackendTokenCalls(ctx, id, 10); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("list scan", func(t *testing.T) {
		s := openTestStore(t)
		defer s.Close()
		a, id := seedVersionUpload(t, s, "o@example.test", "tc-j", 1)
		if err := s.RecordBackendTokenCall(ctx, id, 1, a, 200, false); err != nil {
			t.Fatal(err)
		}
		corruptInt(t, s, "backend_token_calls", "calls")
		_, err := s.ListBackendTokenCalls(ctx, id, 10)
		wantErr(t, "list", err, errScan)
	})
}
