package store

import (
	"context"
	"strings"
	"testing"
)

func TestMigrateAppliedListsEachMigrationOnceInOrder(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	applied, err := s.MigrateApplied(ctx)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(applied)) != schema || schema == 0 {
		t.Fatalf("applied %d migrations, schema is at %d", len(applied), schema)
	}
	for i, m := range applied {
		if m.Version != int64(i+1) || !strings.HasSuffix(m.Source, ".sql") || strings.Contains(m.Source, "/") || m.Duration < 0 {
			t.Errorf("migration %d = %+v", i, m)
		}
	}
	again, err := s.MigrateApplied(ctx)
	if err != nil || len(again) != 0 {
		t.Fatalf("second MigrateApplied = %v, %v; want none", again, err)
	}
}

func TestMigrateAppliedFailsOnAClosedStore(t *testing.T) {
	s := open(t)
	_ = s.Close()
	if applied, err := s.MigrateApplied(context.Background()); err == nil || applied != nil {
		t.Fatalf("MigrateApplied on a closed store = %v, %v", applied, err)
	}
}
