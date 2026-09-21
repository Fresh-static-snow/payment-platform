package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursorRoundTrip(t *testing.T) {
	t.Parallel()
	want := Cursor{CreatedAt: time.Now().UTC().Round(0), ID: uuid.New()}
	got, err := DecodeCursor(EncodeCursor(want))
	if err != nil {
		t.Fatal(err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || got.ID != want.ID {
		t.Fatalf("cursor mismatch: got %+v want %+v", got, want)
	}
}
