package executionapp_test

import (
	"context"
	"errors"
	"testing"

	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/ports"
)

func TestBranches_InvalidFilename(t *testing.T) {
	t.Parallel()
	svc, _, _ := newCollService(t)
	ctx := context.Background()

	if _, err := svc.DownloadFile(ctx, 1, "../x"); !errors.Is(err, executionapp.ErrInvalidFilename) {
		t.Fatalf("DownloadFile(bad) = %v, want ErrInvalidFilename", err)
	}
	if err := svc.DeleteFile(ctx, 1, "a/b"); !errors.Is(err, executionapp.ErrInvalidFilename) {
		t.Fatalf("DeleteFile(bad) = %v, want ErrInvalidFilename", err)
	}
	if err := svc.UploadFile(ctx, 1, "", nil); !errors.Is(err, executionapp.ErrInvalidFilename) {
		t.Fatalf("UploadFile(bad) = %v, want ErrInvalidFilename", err)
	}
}

func TestBranches_Create_InvalidName(t *testing.T) {
	t.Parallel()
	svc, _, _ := newCollService(t)
	if _, err := svc.Create(context.Background(), "", 10, "", "", nil); err == nil {
		t.Fatal("Create(empty name) expected error")
	}
}

func TestBranches_Delete_Missing(t *testing.T) {
	t.Parallel()
	svc, _, _ := newCollService(t)
	if err := svc.Delete(context.Background(), 999); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Delete(missing) = %v, want ErrNotFound", err)
	}
}

func TestBranches_UploadFile_UnknownExecution(t *testing.T) {
	t.Parallel()
	svc, _, _ := newCollService(t)
	ctx := context.Background()
	if err := svc.UploadFile(ctx, 999, "a.csv", nil); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("UploadFile(unknown) = %v, want ErrNotFound", err)
	}
}

func TestBranches_GetConfig_Missing(t *testing.T) {
	t.Parallel()
	svc, _, _ := newCollService(t)
	if _, err := svc.GetConfig(context.Background(), 999); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("GetConfig(missing) = %v, want ErrNotFound", err)
	}
}

func TestBranches_ListByProject(t *testing.T) {
	t.Parallel()
	svc, _, _ := newCollService(t)
	ctx := context.Background()
	if _, err := svc.Create(ctx, "peak", 10, "", "", nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	list, err := svc.ListByProject(ctx, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByProject = %v (err %v), want 1", list, err)
	}
}
