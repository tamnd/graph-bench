package sqlbase

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Nodes calls fn with every node id in the files, and Edges with every
// (src, dst) pair. Both locate their columns by the typed CSV header the
// canonical layout carries, id:ID for a node file and :START_ID and
// :END_ID for a relationship file, rather than by position, because the
// position is a property of the generator and the suffix is a property of
// the format.
//
// A driver uses these when it inserts row by row and ignores them when it
// hands the file to the engine's own CSV reader, which is why they are
// exported rather than wired into the session.
func Nodes(ctx context.Context, files []string, fn func(id int64) error) error {
	return each(ctx, files, []string{":ID"}, func(cells []string) error {
		id, err := strconv.ParseInt(cells[0], 10, 64)
		if err != nil {
			return fmt.Errorf("node id %q is not an integer, and the id columns here are BIGINT: %w", cells[0], err)
		}
		return fn(id)
	})
}

// Edges calls fn with every endpoint pair in the files.
func Edges(ctx context.Context, files []string, fn func(src, dst int64) error) error {
	return each(ctx, files, []string{":START_ID", ":END_ID"}, func(cells []string) error {
		src, err := strconv.ParseInt(cells[0], 10, 64)
		if err != nil {
			return fmt.Errorf("edge start %q is not an integer: %w", cells[0], err)
		}
		dst, err := strconv.ParseInt(cells[1], 10, 64)
		if err != nil {
			return fmt.Errorf("edge end %q is not an integer: %w", cells[1], err)
		}
		return fn(src, dst)
	})
}

// HeaderNames returns the actual header of the column carrying each
// suffix, in the order the suffixes were given: for a node file and
// []string{":ID"} it answers []string{"id:ID"}. An engine that reads the
// CSV itself needs the name rather than the position, because it names
// the columns from the header too and then has to be told which one to
// select.
func HeaderNames(path string, suffixes []string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	head, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read header of %s: %w", path, err)
	}
	names := make([]string, len(suffixes))
	for i, suffix := range suffixes {
		for _, cell := range head {
			if strings.HasSuffix(cell, suffix) {
				names[i] = cell
				break
			}
		}
		if names[i] == "" {
			return nil, fmt.Errorf("%s: header has no %s column", path, suffix)
		}
	}
	return names, nil
}

// each reads the files, locates one column per header suffix, and calls
// fn with those cells for every data row. The context is checked every
// few thousand rows so a cancelled load stops within a load rather than
// at the end of one.
func each(ctx context.Context, files []string, suffixes []string, fn func(cells []string) error) error {
	cells := make([]string, len(suffixes))
	for _, path := range files {
		if err := eachFile(ctx, path, suffixes, cells, fn); err != nil {
			return err
		}
	}
	return nil
}

func eachFile(ctx context.Context, path string, suffixes []string, cells []string, fn func([]string) error) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.ReuseRecord = true
	at := make([]int, len(suffixes))
	for i := range at {
		at[i] = -1
	}
	line := 0
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		line++
		if line == 1 {
			for col, head := range rec {
				for i, suffix := range suffixes {
					if at[i] < 0 && strings.HasSuffix(head, suffix) {
						at[i] = col
					}
				}
			}
			for i, col := range at {
				if col < 0 {
					return fmt.Errorf("%s: header has no %s column", path, suffixes[i])
				}
			}
			continue
		}
		if line%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		for i, col := range at {
			if col >= len(rec) {
				return fmt.Errorf("%s line %d: only %d columns", path, line, len(rec))
			}
			cells[i] = rec[col]
		}
		if err := fn(cells); err != nil {
			return fmt.Errorf("%s line %d: %w", path, line, err)
		}
	}
}

// InsertNodes and InsertEdges are the row-by-row load path, one prepared
// statement inside one transaction. It is the documented way to bulk load
// SQLite and the fallback for anything without a CSV reader of its own.
func InsertNodes(ctx context.Context, db *sql.DB, drv Driver, files []string) (int64, error) {
	stmt := "INSERT INTO node (id) VALUES (" + drv.Placeholder(1) + ")"
	var n int64
	err := inTx(ctx, db, stmt, func(exec func(...any) error) error {
		return Nodes(ctx, files, func(id int64) error {
			if err := exec(id); err != nil {
				return err
			}
			n++
			return nil
		})
	})
	return n, err
}

// InsertEdges is the same for the edge table.
func InsertEdges(ctx context.Context, db *sql.DB, drv Driver, files []string) (int64, error) {
	stmt := "INSERT INTO edge (src, dst) VALUES (" + drv.Placeholder(1) + ", " + drv.Placeholder(2) + ")"
	var n int64
	err := inTx(ctx, db, stmt, func(exec func(...any) error) error {
		return Edges(ctx, files, func(src, dst int64) error {
			if err := exec(src, dst); err != nil {
				return err
			}
			n++
			return nil
		})
	})
	return n, err
}

// inTx prepares one statement inside one transaction and hands the body
// a way to execute it.
func inTx(ctx context.Context, db *sql.DB, stmt string, body func(exec func(...any) error) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	prepared, err := tx.PrepareContext(ctx, stmt)
	if err != nil {
		return fmt.Errorf("prepare %q: %w", stmt, err)
	}
	defer prepared.Close()
	err = body(func(args ...any) error {
		_, err := prepared.ExecContext(ctx, args...)
		return err
	})
	if err != nil {
		return err
	}
	return tx.Commit()
}
