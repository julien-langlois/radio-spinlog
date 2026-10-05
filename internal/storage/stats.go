package storage

import (
	"database/sql"
	"fmt"
	"io"
	"text/tabwriter"
)

// WriteStats prints the latest plays and the totals per radio: a quick check that tracks are being archived
func (s *SQLiteDB) WriteStats(w io.Writer) error {
	fmt.Fprintln(w, "LATEST TRACKS (UTC)")
	err := s.writeTable(w, "TIME\tRADIO\tARTIST\tTITLE", `
		SELECT COALESCE(played_at, scraped_at), radio_slug, artist, title
		FROM tracks ORDER BY id DESC LIMIT 10`)
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "\nTOTALS")
	return s.writeTable(w, "RADIO\tTRACKS\tLAST SCRAPED\t", `
		SELECT radio_slug, COUNT(*), MAX(scraped_at), ''
		FROM tracks GROUP BY radio_slug ORDER BY COUNT(*) DESC`)
}

// writeTable prints a 4-column query result as aligned text
func (s *SQLiteDB) writeTable(w io.Writer, header, query string) error {
	rows, err := s.db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, header)
	for rows.Next() {
		var a, b, c, d sql.RawBytes
		if err := rows.Scan(&a, &b, &c, &d); err != nil {
			return err
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a, b, c, d)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return tw.Flush()
}
