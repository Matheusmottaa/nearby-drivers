package matching

import (
	"context"
	"errors"
	"testing"
)

func TestApplyRecordCommitsOnlyAfterHandle(t *testing.T) {
	pubErr := errors.New("publish failed")
	var committed int

	err := applyRecord(context.Background(), func() error { return pubErr }, func() error {
		committed++
		return nil
	})
	if !errors.Is(err, pubErr) {
		t.Fatalf("applyRecord() error = %v, want %v", err, pubErr)
	}
	if committed != 0 {
		t.Fatalf("committed = %d, want 0", committed)
	}

	err = applyRecord(context.Background(), func() error { return nil }, func() error {
		committed++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if committed != 1 {
		t.Fatalf("committed = %d, want 1", committed)
	}
}
